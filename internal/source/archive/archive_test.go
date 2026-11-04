package archive

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
)

func baseRecord() map[string]any {
	return map[string]any{
		"id": "E1", "subject": "suivi", "sender_name": "Élodie", "sender_address": "elodie@example.org",
		"received_at": "2026-10-12T15:48:34Z", "body_preview": "Salut", "is_read": false, "importance": "1",
		"source": "outlook", "folder": "inbox", "to_addresses": "lucas@example.com, amelie.durand@example.com",
		"cc_addresses": "", "bcc_addresses": nil, "has_attachments": true, "addressed_to_me": "to",
		"sent_on": "2026-10-12T15:48:25Z", "last_verb_executed": "0", "message_flags": "18", "size": "48213",
		"task_due_date": "4501-01-01T00:00:00Z", "spam_confidence": "", "reminder_set": false,
	}
}

func with(base map[string]any, edits map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range edits {
		if v == "<delete>" {
			delete(out, k)
		} else {
			out[k] = v
		}
	}
	return out
}

func writeLines(t *testing.T, gz bool, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mailbox.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var w interface{ Write([]byte) (int, error) } = f
	var z *gzip.Writer
	if gz {
		z = gzip.NewWriter(f)
		w = z
	}
	for _, l := range lines {
		_, _ = w.Write([]byte(l + "\n"))
	}
	if z != nil {
		_ = z.Close()
	}
	return path
}

func line(t *testing.T, rec map[string]any) string {
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

type sink struct {
	msgs     []mail.Message
	skips    map[string]error
	progress []source.Progress
	failOn   int
	onFirst  func()
}

func (s *sink) Message(_ context.Context, m mail.Message) error {
	if s.onFirst != nil && len(s.msgs) == 0 {
		s.onFirst()
	}
	s.msgs = append(s.msgs, m)
	if s.failOn > 0 && len(s.msgs) == s.failOn {
		return errors.New("disk full")
	}
	return nil
}
func (s *sink) Skip(ref string, err error) {
	if s.skips == nil {
		s.skips = map[string]error{}
	}
	s.skips[ref] = err
}
func (s *sink) Progress(p source.Progress) { s.progress = append(s.progress, p) }
func (s *sink) Snapshot(context.Context, []source.Version, bool) (map[string]bool, error) {
	panic("the archive connector never snapshots")
}

func syncFolder(t *testing.T, cfg *config.Config, path, folder string, cursor source.Cursor, sk *sink) (source.Cursor, error) {
	t.Helper()
	c, err := New("legacy", path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	folders, _ := c.Discover(t.Context())
	for _, f := range folders {
		if f.Path == folder {
			return c.Sync(t.Context(), f, cursor, sk)
		}
	}
	t.Fatalf("folder %s not discovered", folder)
	return "", nil
}

func TestMapsAValidRecord(t *testing.T) {
	cfg := configtest.New(t, nil)
	for _, gz := range []bool{false, true} {
		path := writeLines(t, gz, line(t, baseRecord()), "", "   ")
		sk := &sink{}
		cur, err := syncFolder(t, cfg, path, "inbox", "", sk)
		if err != nil || cur == "" || len(sk.msgs) != 1 || len(sk.skips) != 0 {
			t.Fatalf("gz=%v: cursor %q err %v msgs %d skips %v", gz, cur, err, len(sk.msgs), sk.skips)
		}
		m := sk.msgs[0]
		if m.ID != "E1" || m.EntryID != "E1" || m.SourceID != "legacy" || m.FolderRole != mail.RoleInbox || m.Origin != "outlook" ||
			len(m.Recipients) != 2 || m.Recipients[1].Address != "amelie.durand@example.com" || m.Recipients[0].Kind != mail.To ||
			!m.HasAttachments || m.Size != mail.Int(48213) || m.SpamConfidence.Valid || m.TaskDueDate != "" ||
			m.MessageFlags != mail.Int(18) || m.Body != "Salut" || m.BodyTruncated {
			t.Fatalf("mapped message: %+v", m)
		}
		last := sk.progress[len(sk.progress)-1]
		if last.Unit != "bytes" || last.Done != last.Total || last.Total == 0 {
			t.Fatalf("progress %+v", last)
		}
	}
}

func TestFolderRoutingAndCountingOnce(t *testing.T) {
	cfg := configtest.New(t, nil)
	path := writeLines(t, false,
		line(t, baseRecord()),
		line(t, with(baseRecord(), map[string]any{"id": "S1", "folder": "sent"})),
		line(t, with(baseRecord(), map[string]any{"id": "D1", "folder": "<delete>"})),
		line(t, with(baseRecord(), map[string]any{"id": "N1", "folder": nil})),
		line(t, with(baseRecord(), map[string]any{"id": "T1", "folder": "trash"})),
		`{not json`,
		`[1,2]`,
		line(t, with(baseRecord(), map[string]any{"id": "S2", "folder": "sent", "is_read": "true"})),
	)
	inbox, sent := &sink{}, &sink{}
	if _, err := syncFolder(t, cfg, path, "inbox", "", inbox); err != nil {
		t.Fatal(err)
	}
	if _, err := syncFolder(t, cfg, path, "sent", "", sent); err != nil {
		t.Fatal(err)
	}
	if len(inbox.msgs) != 3 || len(sent.msgs) != 1 || sent.msgs[0].FolderRole != mail.RoleSent {
		t.Fatalf("inbox %d sent %d", len(inbox.msgs), len(sent.msgs))
	}
	if len(inbox.skips) != 3 || inbox.skips["line 5"] == nil || inbox.skips["line 6"] == nil || inbox.skips["line 7"] == nil {
		t.Fatalf("unattributable lines belong to the inbox pass: %v", inbox.skips)
	}
	if len(sent.skips) != 1 || !strings.Contains(sent.skips["line 8"].Error(), "boolean") {
		t.Fatalf("sent skips: %v", sent.skips)
	}
}

func TestStrictValidationReportsEveryProblem(t *testing.T) {
	cfg := configtest.New(t, nil)
	bad := with(baseRecord(), map[string]any{
		"extra": 1, "subject": "<delete>", "has_attachments": 1, "sender_name": 5, "size": "12kb",
		"received_at": "12/10/2026", "importance": true,
	})
	path := writeLines(t, false, line(t, bad))
	sk := &sink{}
	if _, err := syncFolder(t, cfg, path, "inbox", "", sk); err != nil {
		t.Fatal(err)
	}
	msg := sk.skips["line 1"].Error()
	for _, want := range []string{"unknown fields: [extra]", `missing field "subject"`, `"has_attachments" must be a boolean`,
		`"sender_name" must be a string`, `"importance" must be a string`} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in %s", want, msg)
		}
	}
	path = writeLines(t, false, line(t, with(baseRecord(), map[string]any{"size": "12kb", "received_at": "12/10/2026"})))
	sk = &sink{}
	_, _ = syncFolder(t, cfg, path, "inbox", "", sk)
	msg = sk.skips["line 1"].Error()
	if !strings.Contains(msg, `"size"`) || !strings.Contains(msg, `"received_at"`) {
		t.Fatalf("mapping errors: %s", msg)
	}
	path = writeLines(t, false, line(t, with(baseRecord(), map[string]any{"id": ""})))
	sk = &sink{}
	_, _ = syncFolder(t, cfg, path, "inbox", "", sk)
	if len(sk.msgs) != 1 || sk.msgs[0].ID != "" {
		t.Fatal("message invariants (an empty id) are the syncer's to judge, not the connector's")
	}
}

func TestBodyTruncationFlag(t *testing.T) {
	cfg := configtest.New(t, map[string]any{"archive.legacy_body_chars": 5})
	path := writeLines(t, false, line(t, with(baseRecord(), map[string]any{"body_preview": "😀😀abc"})))
	sk := &sink{}
	_, _ = syncFolder(t, cfg, path, "inbox", "", sk)
	if !sk.msgs[0].BodyTruncated {
		t.Fatal("5 UTF-16 units (2 surrogate pairs + 1) reach the cap")
	}
}

// The exporter cut every header block at its cap: a block that long is
// declared cut (a header absent from it may lie past the cut); a shorter
// or empty one is not.
func TestHeaderCutIsDeclared(t *testing.T) {
	cfg := configtest.New(t, map[string]any{"archive.legacy_header_chars": 10})
	path := writeLines(t, false, line(t, with(baseRecord(), map[string]any{"headers": "Received: from a.example.org"})),
		line(t, with(baseRecord(), map[string]any{"id": "short", "headers": "X-A: b"})),
		line(t, with(baseRecord(), map[string]any{"id": "none", "headers": ""})))
	sk := &sink{}
	_, _ = syncFolder(t, cfg, path, "inbox", "", sk)
	if len(sk.msgs) != 3 {
		t.Fatalf("%d messages", len(sk.msgs))
	}
	want := []mail.Alteration{{Code: mail.AltHeadersTruncated}}
	if !slices.Equal(sk.msgs[0].Alterations, want) || sk.msgs[1].Alterations != nil || sk.msgs[2].Alterations != nil {
		t.Fatalf("%+v %+v %+v", sk.msgs[0].Alterations, sk.msgs[1].Alterations, sk.msgs[2].Alterations)
	}
}

func TestCursorSkipsUnchangedFile(t *testing.T) {
	cfg := configtest.New(t, nil)
	path := writeLines(t, false, line(t, baseRecord()))
	cur, _ := syncFolder(t, cfg, path, "inbox", "", &sink{})
	sk := &sink{}
	again, err := syncFolder(t, cfg, path, "inbox", cur, sk)
	if err != nil || again != cur || len(sk.msgs) != 0 {
		t.Fatalf("unchanged file must be skipped: %q %v %d", again, err, len(sk.msgs))
	}
	future := time.Now().Add(time.Hour)
	_ = os.Chtimes(path, future, future)
	sk = &sink{}
	if next, _ := syncFolder(t, cfg, path, "inbox", cur, sk); next == cur || len(sk.msgs) != 1 {
		t.Fatal("a touched file must be re-read")
	}
}

func TestFailuresAreFatal(t *testing.T) {
	cfg := configtest.New(t, map[string]any{"archive.max_line_bytes": 1024})
	path := writeLines(t, false, line(t, baseRecord()), line(t, with(baseRecord(), map[string]any{"id": "E2"})))
	if _, err := syncFolder(t, cfg, path, "inbox", "", &sink{failOn: 1}); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("output failure must abort: %v", err)
	}
	changing := &sink{onFirst: func() {
		f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		_, _ = f.WriteString("\n")
		_ = f.Close()
	}}
	if _, err := syncFolder(t, cfg, path, "inbox", "", changing); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("a file changed during the read must not produce a cursor: %v", err)
	}
	long := writeLines(t, false, line(t, with(baseRecord(), map[string]any{"body_preview": strings.Repeat("x", 2000)})))
	if _, err := syncFolder(t, cfg, long, "inbox", "", &sink{}); err == nil || !strings.Contains(err.Error(), "max_line_bytes") {
		t.Fatalf("an over-long line must fail with the setting named: %v", err)
	}
	if _, err := syncFolder(t, cfg, filepath.Join(t.TempDir(), "missing.jsonl"), "inbox", "", &sink{}); err == nil {
		t.Fatal("missing file must fail")
	}
	c, _ := New("legacy", path, cfg)
	if _, err := c.Sync(t.Context(), mail.Folder{Path: "junk"}, "", &sink{}); err == nil {
		t.Fatal("unknown folder must fail")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.Sync(ctx, mail.Folder{Path: "inbox"}, "", &sink{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := New("", path, cfg); err == nil {
		t.Fatal("empty id must be rejected")
	}
	full := writeLines(t, true, line(t, baseRecord()), line(t, with(baseRecord(), map[string]any{"id": "E2"})), line(t, with(baseRecord(), map[string]any{"id": "E3"})))
	raw, _ := os.ReadFile(full)
	cut := filepath.Join(t.TempDir(), "cut.jsonl.gz")
	_ = os.WriteFile(cut, raw[:len(raw)-12], 0o600) // gzip stream truncated mid-file
	if _, err := syncFolder(t, cfg, cut, "inbox", "", &sink{}); err == nil {
		t.Fatal("a truncated gzip stream must fail, not end the import early")
	}
	bad := filepath.Join(t.TempDir(), "bad.gz")
	_ = os.WriteFile(bad, []byte{0x1f, 0x8b, 0, 0}, 0o600)
	if _, err := syncFolder(t, cfg, bad, "inbox", "", &sink{}); err == nil {
		t.Fatal("corrupt gzip must fail")
	}
}

func TestDescribes(t *testing.T) {
	c, _ := New("legacy", "x", configtest.New(t, nil))
	if c.ID() != "legacy" || c.Kind() != Kind || !c.Capabilities().Incremental || c.Capabilities().Bodies {
		t.Fatal("describe")
	}
}
