package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/logx"
)

func logsWith(t testing.TB, n int) *fakeLogs {
	t.Helper()
	f := &fakeLogs{}
	f.entries = make([]logx.Entry, n)
	for i := range f.entries {
		f.entries[i] = entry(uint64(i+1), "retained")
	}
	return f
}

// TestLogsSnapshotIsBounded: the API page is the allocation, not the ring.
func TestLogsSnapshotIsBounded(t *testing.T) {
	logs := logsWith(t, 100_000)
	page, err := logs.Snapshot(LogQuery{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 500 || page.Entries[0].Seq != 99_501 || page.Entries[499].Seq != 100_000 {
		t.Fatalf("page: %d entries, first %d last %d", len(page.Entries), page.Entries[0].Seq, page.Entries[len(page.Entries)-1].Seq)
	}
	if !page.Truncated || page.OldestSeq != 1 || page.LastSeq != 100_000 {
		t.Fatalf("page bounds %+v", page)
	}
	allocs := testing.AllocsPerRun(20, func() {
		page, err := logs.Snapshot(LogQuery{Limit: 500})
		if err != nil || len(page.Entries) != 500 {
			t.Fatalf("page: %v %d", err, len(page.Entries))
		}
	})
	if allocs > 3 {
		t.Fatalf("a 500-entry page allocates %.1f times over 100,000 retained entries", allocs)
	}

	// A since cursor reads forward, and asks only for its own window.
	since, err := logs.Snapshot(LogQuery{Since: 99_500, HasSince: true, Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	if len(since.Entries) != 500 || since.Entries[0].Seq != 99_501 || since.Entries[499].Seq != 100_000 || since.Truncated {
		t.Fatalf("since page: %d entries, first %d, truncated %v", len(since.Entries), since.Entries[0].Seq, since.Truncated)
	}
	forward, err := logs.Snapshot(LogQuery{Since: 0, HasSince: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(forward.Entries) != 10 || forward.Entries[0].Seq != 1 || !forward.Truncated {
		t.Fatalf("forward page: %d entries, first %d, truncated %v", len(forward.Entries), forward.Entries[0].Seq, forward.Truncated)
	}
}

// TestLogsPageCanOmitAttrs: the Logs screen's page carries scalars and the
// has_attrs projection fact, never the attribute maps, so one expanded row
// can read its own entry. The lean page must stay under half the full page.
func TestLogsPageCanOmitAttrs(t *testing.T) {
	const count = 500
	logs := &fakeLogs{entries: make([]logx.Entry, count)}
	for i := range logs.entries {
		logs.entries[i] = entry(uint64(i+1), "retained")
		logs.entries[i].Attrs = map[string]any{
			"attempt":    1,
			"duration":   "12.4ms",
			"mailbox":    "owner@example.test",
			"message_id": "mail-000123",
			"provider":   "systemone",
			"retry":      1,
			"stage":      "triage",
			"stream":     "owner",
		}
	}
	w := newWorld()
	w.logs = logs
	w.services.Logs = logs
	srv, ts, _ := newTestServerWith(t, testOptions(t), w)

	status, _, full := do(t, ts, srv, request{method: "GET", path: "/api/logs?limit=500"})
	mustStatus(t, status, 200, full)
	status, _, lean := do(t, ts, srv, request{method: "GET", path: "/api/logs?limit=500&attrs=off"})
	mustStatus(t, status, 200, lean)
	if strings.Contains(string(full), "has_attrs") {
		t.Fatal("the default page carries the attrs=off projection field")
	}

	type wireEntry struct {
		Seq       uint64         `json:"seq"`
		Time      string         `json:"time"`
		Level     string         `json:"level"`
		Component string         `json:"component"`
		Msg       string         `json:"msg"`
		Attrs     map[string]any `json:"attrs"`
		HasAttrs  bool           `json:"has_attrs"`
	}
	var fullPage, leanPage struct {
		Entries []wireEntry `json:"entries"`
	}
	if err := json.Unmarshal(full, &fullPage); err != nil {
		t.Fatalf("full page: %v", err)
	}
	if err := json.Unmarshal(lean, &leanPage); err != nil {
		t.Fatalf("lean page: %v", err)
	}
	if len(fullPage.Entries) != count || len(leanPage.Entries) != count {
		t.Fatalf("pages: %d full entries, %d lean entries", len(fullPage.Entries), len(leanPage.Entries))
	}
	for i := range fullPage.Entries {
		entry := fullPage.Entries[i]
		if len(entry.Attrs) == 0 || entry.HasAttrs {
			t.Fatalf("full entry %d: attrs %v, has_attrs %v", i, entry.Attrs, entry.HasAttrs)
		}
	}
	for i, entry := range leanPage.Entries {
		if entry.Attrs != nil {
			t.Fatalf("lean entry %d still carries attrs: %v", i, entry.Attrs)
		}
		if !entry.HasAttrs {
			t.Fatalf("lean entry %d: has_attrs is false", i)
		}
		if entry.Seq != uint64(i+1) || entry.Time == "" || entry.Level != "INFO" ||
			entry.Component != "test" || entry.Msg != "retained" {
			t.Fatalf("lean entry %d lost a scalar field: %+v", i, entry)
		}
	}
	t.Logf("logs page: %d bytes with attrs, %d bytes attrs=off (%.1f%%)",
		len(full), len(lean), 100*float64(len(lean))/float64(len(full)))
	if len(lean)*2 >= len(full) {
		t.Fatalf("attrs=off page is %d bytes of %d, not under half", len(lean), len(full))
	}

	// An entry without attributes is marked false, so the client knows not
	// to fetch it.
	with := entry(1, "with")
	with.Attrs = map[string]any{"key": "value"}
	logs.Reset(with, entry(2, "without"))
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/logs?attrs=off&limit=2"})
	mustStatus(t, status, 200, data)
	var mixed struct {
		Entries []wireEntry `json:"entries"`
	}
	if err := json.Unmarshal(data, &mixed); err != nil {
		t.Fatalf("mixed page: %v", err)
	}
	if len(mixed.Entries) != 2 || !mixed.Entries[0].HasAttrs || mixed.Entries[1].HasAttrs {
		t.Fatalf("has_attrs marks: %+v", mixed.Entries)
	}
}

// TestLogsEntryReturnsOneEntryWithAttrs: the lazy read returns the entry as
// stored, attributes included, and nothing else in a page wrapper.
func TestLogsEntryReturnsOneEntryWithAttrs(t *testing.T) {
	w := newWorld()
	second := entry(2, "second")
	second.Attrs = map[string]any{"attempt": 2, "mailbox": "owner@example.test"}
	w.logs.Reset(entry(1, "first"), second, entry(3, "third"))
	srv, ts, _ := newTestServerWith(t, testOptions(t), w)

	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/logs/2"})
	mustStatus(t, status, 200, data)
	if _, ok := decodeMap(t, data)["entries"]; ok {
		t.Fatal("the one-entry route returned a page")
	}
	var got logx.Entry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("entry: %v", err)
	}
	if got.Seq != 2 || got.Message != "second" || len(got.Attrs) != 2 ||
		got.Attrs["mailbox"] != "owner@example.test" {
		t.Fatalf("entry: %+v", got)
	}
}

// TestLogsEntryRotatedOutIs404: a sequence outside the retained window is
// gone, never the next retained entry.
func TestLogsEntryRotatedOutIs404(t *testing.T) {
	w := newWorld()
	w.logs.Reset(entry(101, "oldest"), entry(102, "newest"))
	srv, ts, _ := newTestServerWith(t, testOptions(t), w)

	for _, path := range []string{"/api/logs/1", "/api/logs/50", "/api/logs/999"} {
		status, _, data := do(t, ts, srv, request{method: "GET", path: path})
		mustStatus(t, status, 404, data)
		if code := errorCode(t, data); code != "log_not_found" {
			t.Fatalf("%s: code %q", path, code)
		}
		if _, ok := decodeMap(t, data)["entries"]; ok {
			t.Fatalf("%s: a 404 carried entries", path)
		}
	}

	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/logs/102"})
	mustStatus(t, status, 200, data)
}

// TestLogsPageMaximumIsIndependentOfTheRing: the same API limit is valid
// with a tiny or a huge ring, and one above the page maximum is rejected
// rather than clamped.
func TestLogsPageMaximumIsIndependentOfTheRing(t *testing.T) {
	for _, size := range []int{10, 100_000} {
		w := newWorld()
		w.logs = logsWith(t, size)
		w.services.Logs = w.logs
		srv, ts, _ := newTestServerWith(t, testOptions(t), w)
		status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/logs?limit=5000"})
		mustStatus(t, status, 200, data)
		status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/logs?limit=5001"})
		mustStatus(t, status, 400, data)
		if code := errorCode(t, data); code != "invalid_limit" {
			t.Fatalf("ring %d: code %q", size, code)
		}
	}
}
