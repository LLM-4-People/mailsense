package syncer

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
)

type logRecord struct {
	Level string
	Msg   string
	Attrs map[string]any
}

// decodeJSONLogs decodes the records an unredacted slog JSON handler wrote.
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
			case "time":
			case "level":
				rec.Level, _ = v.(string)
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

// TestSourceLogsDoNotCarryMailboxNames: the scope log identifies a store by
// its stable source:store key and kind, never by its localized display name.
func TestSourceLogsDoNotCarryMailboxNames(t *testing.T) {
	s, _, _, _ := setup(t, nil)
	var buf bytes.Buffer
	s.log = slog.New(slog.NewJSONHandler(&buf, nil))
	c := &fakeConn{id: "fake", noOwner: true, items: map[string][]item{"Inbox": items(1, inbox, "i")}}
	if _, err := s.Run(t.Context(), c, nil); err != nil {
		t.Fatal(err)
	}
	records := decodeJSONLogs(t, &buf)
	var blocked, scope bool
	for _, r := range records {
		switch r.Msg {
		case "mailbox has no owner identity; its mail is not judged":
			blocked = true
			if r.Attrs["store"] != fakeKey || r.Attrs["kind"] != "primary" {
				t.Errorf("blocked-store log lost its stable identity: %#v", r.Attrs)
			}
		case "store scope":
			scope = true
			if r.Attrs["store"] != fakeKey || r.Attrs["kind"] != "primary" {
				t.Errorf("scope log lost its stable identity: %#v", r.Attrs)
			}
			if r.Attrs["address_count"] != float64(0) || r.Attrs["judged"] != float64(0) {
				t.Errorf("scope diagnostics changed: %#v", r.Attrs)
			}
		}
	}
	if !blocked || !scope {
		t.Fatalf("blocked=%v scope=%v records=%s", blocked, scope, buf.String())
	}
	if strings.Contains(buf.String(), "Amélie") {
		t.Fatalf("mailbox display name reached the log: %s", buf.String())
	}
}

// TestSourceLogsDoNotCarryAlterationDetail: an alteration's detail can be an
// address header; the log keeps the code class and the detail length only.
func TestSourceLogsDoNotCarryAlterationDetail(t *testing.T) {
	s, _, _, _ := setup(t, nil)
	var buf bytes.Buffer
	s.log = slog.New(slog.NewJSONHandler(&buf, nil))
	const detail = "X-Delivery-To: alice@example.test"
	m := mailtest.Message("fake", "altered-1")
	m.Alter(mail.AltAddressUnparsed, detail)
	c := &fakeConn{id: "fake", items: map[string][]item{"Inbox": {{msg: m}}}}
	if _, err := s.Run(t.Context(), c, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), detail) || strings.Contains(buf.String(), "alice@example.test") {
		t.Fatalf("raw alteration detail reached the log: %s", buf.String())
	}
	var found bool
	for _, r := range decodeJSONLogs(t, &buf) {
		if r.Msg != "message altered while read" {
			continue
		}
		found = true
		if r.Attrs["code"] != mail.AltAddressUnparsed {
			t.Errorf("code = %#v", r.Attrs["code"])
		}
		if r.Attrs["detail_bytes"] != float64(len(detail)) {
			t.Errorf("detail_bytes = %#v, want %d", r.Attrs["detail_bytes"], len(detail))
		}
		if _, ok := r.Attrs["detail"]; ok {
			t.Errorf("raw detail attribute is still present: %#v", r.Attrs)
		}
	}
	if !found {
		t.Fatalf("no alteration log: %s", buf.String())
	}
}
