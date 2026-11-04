package outlook

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
)

type logRecord struct {
	Msg   string
	Attrs map[string]any
}

func decodeJSONLogs(t *testing.T, buf *bytes.Buffer) []logRecord {
	t.Helper()
	var out []logRecord
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			t.Fatalf("log line is not JSON: %v", err)
		}
		rec := logRecord{Attrs: map[string]any{}}
		for k, v := range raw {
			switch k {
			case "time", "level":
			case "msg":
				rec.Msg, _ = v.(string)
			default:
				rec.Attrs[k] = v
			}
		}
		out = append(out, rec)
	}
	return out
}

// TestPhaseLogUsesStableStoreIdentity: a phase log names the store by its
// stable key and kind, never by its display name.
func TestPhaseLogUsesStableStoreIdentity(t *testing.T) {
	c, _ := testConnector(t, profile(), nil)
	var buf bytes.Buffer
	c.log = slog.New(slog.NewJSONHandler(&buf, nil))
	c.publish(Phase{Phase: source.PhaseSnapshot, Store: "Alex's mailbox", StoreKey: "mbx-123",
		Kind: mail.StorePrimary, Folder: "Inbox", Rows: 5, Calls: 2, Duration: time.Second})
	if strings.Contains(buf.String(), "Alex") {
		t.Fatalf("store display name reached the log: %s", buf.String())
	}
	var found bool
	for _, r := range decodeJSONLogs(t, &buf) {
		if r.Msg != "phase" {
			continue
		}
		found = true
		if r.Attrs["store_key"] != "mbx-123" || r.Attrs["kind"] != "primary" || r.Attrs["rows"] != float64(5) {
			t.Errorf("phase diagnostics changed: %#v", r.Attrs)
		}
		if _, ok := r.Attrs["store"]; ok {
			t.Errorf("raw store attribute is still present: %#v", r.Attrs)
		}
	}
	if !found {
		t.Fatalf("no phase log: %s", buf.String())
	}
}

// TestCalendarSeriesWarningHasNoSubject: a recurring series that is skipped
// is identified by a digest of its EntryID and the pattern type, never by the
// calendar subject.
func TestCalendarSeriesWarningHasNoSubject(t *testing.T) {
	o := profile()
	cal := &fakeFolder{name: "Calendrier", eid: hexID("cal-primary"), class: "IPF.Appointment", itemType: 1}
	master := eventItem("series-secret-id", "Board salary review", "2026-11-03T14:00:00Z", "2026-11-03T15:00:00Z")
	master.props["IsRecurring"] = true
	master.props["RecurrenceType"] = int64(4) // not an OlRecurrenceType: the series is skipped
	cal.items = []*fakeItem{master}
	o.stores[1].defaults[9] = cal
	o.stores[1].root.children = append(o.stores[1].root.children, cal)
	c, _ := testConnector(t, o, nil)
	if _, err := c.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	cols, err := c.CalendarCollections(t.Context())
	if err != nil || len(cols) != 1 {
		t.Fatalf("collections %+v: %v", cols, err)
	}
	var buf bytes.Buffer
	c.log = slog.New(slog.NewJSONHandler(&buf, nil))
	since, _ := time.Parse(time.RFC3339, "2026-11-01T00:00:00Z")
	until, _ := time.Parse(time.RFC3339, "2026-12-01T00:00:00Z")
	err = c.SyncCalendar(t.Context(), cols[0], since, until, &calendarSink{})
	var incomplete *IncompleteCalendarError
	if err != nil && !errors.As(err, &incomplete) {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "Board salary review") || strings.Contains(buf.String(), "series-secret-id") {
		t.Fatalf("series subject or raw id reached the log: %s", buf.String())
	}
	var found bool
	for _, r := range decodeJSONLogs(t, &buf) {
		if r.Msg != "calendar: series skipped" {
			continue
		}
		found = true
		id, _ := r.Attrs["series_id"].(string)
		if len(id) != 12 {
			t.Errorf("series_id = %#v, want a 12-hex-digit digest", r.Attrs["series_id"])
		}
		if r.Attrs["pattern"] != "type 4" {
			t.Errorf("pattern = %#v, want the pattern type", r.Attrs["pattern"])
		}
		if reason, _ := r.Attrs["reason"].(string); !strings.Contains(reason, "unsupported recurrence type") {
			t.Errorf("reason = %#v, want the bounded skip reason", r.Attrs["reason"])
		}
		if _, ok := r.Attrs["series"]; ok {
			t.Errorf("raw series attribute is still present: %#v", r.Attrs)
		}
	}
	if !found {
		t.Fatalf("no series warning: %s", buf.String())
	}
}

// TestDiscoverLogUsesAddressCount: discovery keeps the operational owner
// address count, under a key the content policy does not deny.
func TestDiscoverLogUsesAddressCount(t *testing.T) {
	c, _ := testConnector(t, profile(), nil)
	var buf bytes.Buffer
	c.log = slog.New(slog.NewJSONHandler(&buf, nil))
	if _, err := c.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range decodeJSONLogs(t, &buf) {
		if r.Msg != "store" {
			continue
		}
		found = true
		if _, ok := r.Attrs["addresses"]; ok {
			t.Errorf("discovery still logs the denied addresses key: %#v", r.Attrs)
		}
		if _, ok := r.Attrs["address_count"]; !ok {
			t.Errorf("discovery lost the owner address count: %#v", r.Attrs)
		}
	}
	if !found {
		t.Fatalf("no discovery store log: %s", buf.String())
	}
}
