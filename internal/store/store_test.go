package store

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/fsx"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/source"
)

func open(t *testing.T, overrides map[string]any) (*Store, *config.Config) {
	t.Helper()
	if _, ok := overrides[source.SettingSelected]; !ok {
		overrides = maps.Clone(overrides)
		if overrides == nil {
			overrides = map[string]any{}
		}
		overrides[source.SettingSelected] = mailtest.Picked([]string{"src"}, [2]string{"src", "S1"}, [2]string{"src", "S2"},
			[2]string{"src", "s"}, [2]string{"src", "x"}, [2]string{"src", "store-2"})
	}
	cfg := configtest.New(t, overrides)
	template, err := migratedTemplate()
	if err != nil {
		t.Fatalf("store: migrated template: %v", err)
	}
	if err := os.WriteFile(Path(cfg), template, fsx.FilePerm); err != nil {
		t.Fatalf("store: copy migrated template: %v", err)
	}
	s, err := Open(t.Context(), cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.UpsertSource(t.Context(), "src", "test", "Test", source.Capabilities{}); err != nil {
		t.Fatal(err)
	}
	return s, cfg
}

func TestOpenMigratesAndIsIdempotent(t *testing.T) {
	s, cfg := open(t, nil)
	var v int
	all, _ := migrations()
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != len(all) || v < 2 {
		t.Fatalf("user_version %d: %v", v, err)
	}
	var mode string
	_ = s.db.QueryRow("PRAGMA journal_mode").Scan(&mode)
	if mode != "wal" {
		t.Fatalf("journal mode %q", mode)
	}
	_ = s.Close()
	again, err := Open(t.Context(), cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = again.Close()
}

// TestMessageIndexesFollowTheirQueryOwners: the raw sender index has no
// reader, and the move-survivor lookup has one composite owner.
func TestMessageIndexesFollowTheirQueryOwners(t *testing.T) {
	s, _ := open(t, nil)
	rows, err := s.db.Query("SELECT name FROM sqlite_master WHERE type = 'index' AND tbl_name = 'messages'")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	if names["messages_sender"] {
		t.Fatal("the unused raw sender index is still present")
	}
	if !names["messages_survivor"] {
		t.Fatalf("the move-survivor index is missing from %v", names)
	}
	cols, err := s.db.Query("PRAGMA index_info(messages_survivor)")
	if err != nil {
		t.Fatal(err)
	}
	defer cols.Close()
	var got []string
	for cols.Next() {
		var seqno, cid int
		var name string
		if err := cols.Scan(&seqno, &cid, &name); err != nil {
			t.Fatal(err)
		}
		got = append(got, name)
	}
	want := []string{"source_id", "store_id", "internet_message_id"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("messages_survivor columns = %v, want %v", got, want)
	}
}

func TestColumnsMatchSchema(t *testing.T) {
	s, _ := open(t, nil)
	rows, err := s.db.Query("SELECT name FROM pragma_table_info('messages')")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	fixed := map[string]bool{"id": true, "source_id": true, "store_id": true, "msg_id": true, "folder_id": true,
		"first_seen_at": true, "updated_at": true, "last_seen_run": true, "revision": true,
		// Display-only columns outside the learning model's mapping
		// (mail.Message keeps BodyHTML json:"-" and is loaded explicitly).
		"body_html": true, "body_html_known": true, "body_html_truncated": true,
		// The display backfill's materialized marker and completion marker
		// (migrations 0027-0028), owned by the store and never part of the
		// message model.
		"has_inline_refs": true, "inline_assets_checked": true,
		// The materialized mail.Instant (migration 0021) and the sender
		// keys (migration 0031), written by the one writer and never part of
		// the JSON mapping.
		"instant": true, "sender_norm": true, "sender_domain": true}
	var got []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		if !fixed[n] {
			got = append(got, n)
		}
	}
	if !reflect.DeepEqual(got, messageColumns.names) {
		t.Fatalf("schema columns\n%v\ndiffer from mail.Message mapping\n%v", got, messageColumns.names)
	}
}

func TestUpsertRoundTripAndUpdate(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	m := mailtest.Message("src", "1")
	if err := s.UpsertMessages(ctx, 0, []mail.Message{m}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetMessage(ctx, "src", "store-1", "1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, m) {
		t.Fatalf("round trip differs:\n got %+v\nwant %+v", got, m)
	}
	m.Subject, m.Recipients, m.Importance, m.IsRead = "Changed", []mail.Recipient{{Kind: mail.Bcc, Address: "z@z"}}, mail.OptInt{}, false
	if err := s.UpsertMessages(ctx, 0, []mail.Message{m}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetMessage(ctx, "src", "store-1", "1")
	if !reflect.DeepEqual(got, m) {
		t.Fatalf("update differs:\n got %+v\nwant %+v", got, m)
	}
	res, _ := s.Search(ctx, "Réunion", 10)
	if res.Total != 0 {
		t.Fatal("FTS must follow the update (old subject gone)")
	}
	if res, _ = s.Search(ctx, "changed", 10); res.Total != 1 {
		t.Fatal("FTS must index the new subject")
	}
	if _, err := s.GetMessage(ctx, "src", "store-1", "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing message: %v", err)
	}
	counts, _ := s.CountByRole(ctx)
	if counts[mail.RoleInbox] != 1 {
		t.Fatalf("counts %v", counts)
	}
}

func TestInvalidMessageFailsWholeBatch(t *testing.T) {
	s, _ := open(t, nil)
	good, bad := mailtest.Message("src", "1"), mailtest.Message("src", "2")
	bad.ReceivedAt = "12/10/2026"
	if err := s.UpsertMessages(t.Context(), 0, []mail.Message{good, bad}); err == nil {
		t.Fatal("expected a validation error")
	}
	if counts, _ := s.CountByRole(t.Context()); len(counts) != 0 {
		t.Fatalf("nothing may be written: %v", counts)
	}
	orphan := mailtest.Message("unknown-source", "1")
	if err := s.UpsertMessages(t.Context(), 0, []mail.Message{orphan}); err == nil {
		t.Fatal("a message of an unregistered source must be rejected (FK)")
	}
}

func TestSearchIsAccentInsensitiveAndSafe(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	var batch []mail.Message
	for _, id := range []string{"a", "b", "c"} {
		batch = append(batch, mailtest.Message("src", id))
	}
	if err := s.UpsertMessages(ctx, 0, batch); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"réunion", "reunion", "REUNION", "ébauche", "ebauche", "oeuvre", "œuvre", "elodie@example.org", "lucas"} {
		res, err := s.Search(ctx, q, 2) // œ folds to oe (mail/fold.json): both spellings match
		if err != nil || res.Total != 3 || len(res.Hits) != 2 {
			t.Errorf("%q: total %d hits %d err %v", q, res.Total, len(res.Hits), err)
		}
	}
	for _, q := range []string{`NEAR(a b)`, `"unbalanced`, `*`, `subject:x OR`, `a AND`, `-`, `^x`} {
		if _, err := s.Search(ctx, q, 5); err != nil {
			t.Errorf("operator text %q must be neutralised, got %v", q, err)
		}
	}
	if _, err := s.Search(ctx, `  "" `, 5); err == nil {
		t.Error("empty search must fail")
	}
}

func TestLockedDatabase(t *testing.T) {
	s, cfg := open(t, map[string]any{"store.busy_timeout": "50ms"})
	ctx := t.Context()
	if err := s.UpsertMessages(ctx, 0, []mail.Message{mailtest.Message("src", "1")}); err != nil {
		t.Fatal(err)
	}
	locker, err := sql.Open("sqlite", dsn(Path(cfg), url.Values{}))
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Close()
	conn, _ := locker.Conn(ctx)
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	err = s.UpsertMessages(ctx, 0, []mail.Message{mailtest.Message("src", "2")})
	if !IsBusy(err) {
		t.Fatalf("want a busy error, got %v", err)
	}
	_, _ = conn.ExecContext(ctx, "ROLLBACK")
	if _, err := s.GetMessage(ctx, "src", "store-1", "1"); err != nil {
		t.Fatalf("prior data must survive: %v", err)
	}
	if IsBusy(errors.New("x")) || IsBusy(nil) {
		t.Fatal("IsBusy false positives")
	}
}

func TestCorruptAndFutureDatabasesAreRefused(t *testing.T) {
	cfg := configtest.New(t, nil)
	if err := os.WriteFile(Path(cfg), []byte("this is not a sqlite database, just text padding........................................"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(t.Context(), cfg, nil, nil); err == nil {
		t.Fatal("a corrupt file must be refused")
	}
	cfg2 := configtest.New(t, nil)
	s, err := Open(t.Context(), cfg2, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.db.Exec("PRAGMA user_version = 99")
	_ = s.Close()
	if _, err := Open(t.Context(), cfg2, nil, nil); err == nil {
		t.Fatal("a newer schema must be refused")
	}
}

func TestPathWithURISpecialCharacters(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "we?ird #dir %20")
	cfg := configtest.New(t, map[string]any{"store.file": filepath.Join(dir, "m?.db")})
	s, err := Open(t.Context(), cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if _, err := os.Stat(filepath.Join(dir, "m?.db")); err != nil {
		t.Fatalf("database not created at the exact path: %v", err)
	}
	if runtime.GOOS == "windows" {
		if got := fileURI(`C:\Users\me\My Data\m.db`); got != "file:///C:/Users/me/My%20Data/m.db" {
			t.Fatalf("windows URI: %s", got)
		}
	}
	if got := fileURI("/a b/c?d#e%f.db"); got != "file:///a%20b/c%3Fd%23e%25f.db" {
		t.Fatalf("URI: %s", got)
	}
}

func TestFoldersCursorsRuns(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	f := mail.Folder{SourceID: "src", StoreID: "s", StoreName: "S", Path: "Inbox", Role: mail.RoleInbox, ItemCount: 10, StoreKind: mail.StoreOther}
	id, err := s.UpsertFolder(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	f.ItemCount = -1
	if id2, _ := s.UpsertFolder(ctx, f); id2 != id {
		t.Fatal("folder upsert must be stable")
	}
	var count int
	_ = s.db.QueryRow("SELECT item_count FROM folders WHERE id = ?", id).Scan(&count)
	if count != 10 {
		t.Fatal("unknown count must not overwrite a known one")
	}
	for _, bad := range []mail.Folder{{SourceID: "src", Path: "x", Role: "nope"}, {SourceID: "src", Role: mail.RoleInbox}} {
		if _, err := s.UpsertFolder(ctx, bad); err == nil {
			t.Errorf("folder %+v must be rejected", bad)
		}
	}
	if _, ok, _ := s.Cursor(ctx, id); ok {
		t.Fatal("no cursor yet")
	}
	_ = s.SetCursors(ctx, map[int64]string{id: "c1"})
	_ = s.SetCursors(ctx, map[int64]string{id: "c2"})
	if c, ok, _ := s.Cursor(ctx, id); !ok || c != "c2" {
		t.Fatalf("cursor %q", c)
	}
	run, err := s.BeginRun(ctx, "src")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRun(ctx, run, runstate.Failed, RunTotals{Seen: 3}, errors.New("boom")); err != nil {
		t.Fatal(err)
	}
	runs, _ := s.ListRuns(ctx, 10)
	if len(runs) != 1 || runs[0].State != runstate.Failed || runs[0].Totals.Seen != 3 || runs[0].Error != "boom" {
		t.Fatalf("runs %+v", runs)
	}
	if err := s.FinishRun(ctx, 999, runstate.Failed, RunTotals{}, nil); err == nil {
		t.Fatal("finishing an unknown run must fail")
	}
	if err := s.UpsertSource(ctx, "", "k", "", source.Capabilities{}); err == nil {
		t.Fatal("empty source id must be rejected")
	}
}

// #5: every language of the synthetic corpus is found by a word of its
// own — scripts without spaces through the substring index, a term
// shorter than a trigram by a disclosed scan — and the count is capped.
func TestSearchFindsEveryScript(t *testing.T) {
	s, _ := open(t, map[string]any{"search.count_cap": 3})
	ctx := t.Context()
	var batch []mail.Message
	for i, c := range mailtest.Corpus() {
		m := mailtest.Message("src", fmt.Sprint("ml", i))
		m.Subject, m.Body, m.SenderAddress = c.Subject, c.Body, c.Sender
		batch = append(batch, m)
	}
	if err := s.UpsertMessages(ctx, 0, batch); err != nil {
		t.Fatal(err)
	}
	for i, c := range mailtest.Corpus() {
		res, err := s.Search(ctx, c.Term, 5)
		if err != nil || res.Total != 1 || len(res.Hits) != 1 || res.Hits[0].ID != fmt.Sprint("ml", i) {
			t.Errorf("%s %q: %+v %v", c.Lang, c.Term, res, err)
		}
		want := ModeWord
		if mail.NeedsSubstringSearch(c.Term) {
			want = ModeSubstring
		}
		if res.Terms[0].Mode != want {
			t.Errorf("%s: mode %s, want %s", c.Lang, res.Terms[0].Mode, want)
		}
	}
	for q, want := range map[string]TermMode{"会议": ModeScan, "报告": ModeScan, "進度": ModeScan} {
		res, _ := s.Search(ctx, q, 5)
		if res.Terms[0].Mode != want {
			t.Errorf("%q: %+v", q, res.Terms)
		}
	}
	if res, _ := s.Search(ctx, "报告", 5); res.Total != 1 {
		t.Fatalf("a two-character Chinese word is found by the scan: %+v", res)
	}
	if res, _ := s.Search(ctx, "进度报告 谢谢", 5); res.Total != 1 || res.Terms[1].Mode != ModeScan {
		t.Fatalf("mixed substring and scan terms: %+v", res)
	}
	if res, _ := s.Search(ctx, "ΣΥΝΑΝΤΗΣΗ", 5); res.Total != 1 {
		t.Fatal("Greek is found without its tonos and in capitals")
	}
	if res, _ := s.Search(ctx, "omar@مثال.إختبار", 5); res.Total != 1 {
		t.Fatal("an IDN address is searchable")
	}
	res, _ := s.Search(ctx, "budget", 5) // every message's subject mentions none; bodies of the corpus do once
	if res.TotalCapped {
		t.Fatalf("under the cap: %+v", res)
	}
	all, _ := s.Search(ctx, "example.org", 5)
	if all.Total != 3 || !all.TotalCapped {
		t.Fatalf("the count stops at search.count_cap: %+v", all)
	}
}

// #9: attachments' metadata and further folders are stored with their
// message, replaced on rewrite and read back; a folder counts the
// messages that are in it through either.
func TestAttachmentsAndFolderMemberships(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	m := mailtest.Message("src", "g1")
	m.Attachments = []mail.Attachment{{Name: "facture-é.pdf", ContentType: "application/pdf", Size: 1234}, {Name: "logo.png", ContentType: "image/png", Size: -1, Inline: true}}
	m.Folders = []mail.FolderRef{{Path: "Important", Role: mail.RoleOther}, {Path: "Clients/ACME", Role: mail.RoleArchive}}
	if err := s.UpsertMessages(ctx, 0, []mail.Message{m}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetMessage(ctx, "src", "store-1", "g1")
	if err != nil || !reflect.DeepEqual(got.Attachments, m.Attachments) || len(got.Folders) != 2 || got.Folders[0].Path != "Clients/ACME" {
		t.Fatalf("%+v %+v %v", got.Attachments, got.Folders, err)
	}
	members, _ := s.FolderMembers(ctx, "src")
	if len(members) != 3 {
		t.Fatalf("inbox + two labels: %v", members)
	}
	m.Attachments, m.Folders = nil, []mail.FolderRef{{Path: "Important", Role: mail.RoleOther}}
	if err := s.UpsertMessages(ctx, 0, []mail.Message{m}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetMessage(ctx, "src", "store-1", "g1"); got.Attachments != nil || len(got.Folders) != 1 {
		t.Fatalf("a rewrite replaces both: %+v", got)
	}
}

// A restored or hand-made data directory can be group/world accessible while
// holding every message body. Open repairs the directory and the database
// and its side files to the application's private modes.
func TestOpenRepairsPrivateModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows uses per-user ACLs, not POSIX modes")
	}
	cfg := configtest.New(t, nil)
	dir := filepath.Dir(Path(cfg))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(t.Context(), cfg, nil, nil); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("data directory mode %v (%v), want 0700", info.Mode().Perm(), err)
	}
	for _, name := range []string{Path(cfg), Path(cfg) + "-wal", Path(cfg) + "-shm"} {
		info, err := os.Stat(name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v, want 0600", name, info.Mode().Perm())
		}
	}
}

// A panic inside the transaction body must roll back before the connection is
// returned to the pool: an open transaction would keep the write lock and
// every later write would fail busy.
func TestInTxOnRollsBackAfterPanic(t *testing.T) {
	s, _ := open(t, map[string]any{
		"store.busy_timeout":        "20ms",
		"index.store_busy_attempts": 1,
	})
	ctx := t.Context()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the transaction body did not panic")
			}
		}()
		_ = inTxOn(ctx, conn, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, "INSERT INTO meta (key, value) VALUES ('panic-probe', '1')"); err != nil {
				t.Fatalf("insert in the panicking transaction: %v", err)
			}
			panic("boom")
		})
	}()

	var value string
	if err := s.db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = 'panic-probe'").Scan(&value); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("the panicking transaction committed: value %q, err %v", value, err)
	}
	if err := s.UpsertMessages(ctx, 0, []mail.Message{mailtest.Message("src", "after-panic")}); err != nil {
		t.Fatalf("a write after the recovered panic failed: %v", err)
	}
}
