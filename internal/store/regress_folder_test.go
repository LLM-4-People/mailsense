package store

import (
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/buildinfo"
	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// Review 1a (low): the message write path overwrote folder metadata set by Discover.
func TestMessageWriteKeepsDiscoveredFolderMetadata(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	id, err := s.UpsertFolder(ctx, mail.Folder{SourceID: "src", StoreID: "store-1", StoreName: "Archive Store", Path: "Inbox", Role: mail.RoleArchive, ItemCount: 5, StoreKind: mail.StoreOther})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertMessages(ctx, 0, []mail.Message{mailtest.Message("src", "1")}); err != nil {
		t.Fatal(err)
	}
	var role, name string
	var count int
	_ = s.db.QueryRow("SELECT role, store_name, item_count FROM folders WHERE id = ?", id).Scan(&role, &name, &count)
	if role != string(mail.RoleArchive) || name != "Archive Store" || count != 5 {
		t.Fatalf("discovered metadata overwritten: role=%s name=%s count=%d", role, name, count)
	}
}

func TestMetaEventsListsAndMaintenance(t *testing.T) {
	bus := events.New()
	migrated, _ := events.Subscribe(bus, TopicMigrated, 4)
	runs, _ := events.Subscribe(bus, TopicRunChanged, 4)
	cfg := configtest.New(t, nil)
	clk := clocktest.New(time.Date(2026, 10, 14, 8, 0, 0, 0, time.UTC))
	s, err := Open(t.Context(), cfg, bus, clk)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := t.Context()
	if m := <-migrated.C; m.Version != 1 {
		t.Fatalf("migration event %+v", m)
	}
	meta, _ := s.Meta(ctx)
	if meta["created_at"] != "2026-10-14T08:00:00Z" || meta["created_version"] != buildinfo.Version || meta["last_open_at"] != meta["created_at"] {
		t.Fatalf("meta %v", meta)
	}
	clk.Advance(time.Hour)
	_ = s.recordOpen(ctx)
	if meta, _ = s.Meta(ctx); meta["created_at"] != "2026-10-14T08:00:00Z" || meta["last_open_at"] != "2026-10-14T09:00:00Z" {
		t.Fatalf("reopen must keep created_* and update last_open_*: %v", meta)
	}
	_ = s.UpsertSource(ctx, "src", "test", "Test", source.Capabilities{})
	run, _ := s.BeginRun(ctx, "src")
	if ev := <-runs.C; ev.RunID != run || ev.State != runstate.Running {
		t.Fatalf("run event %+v", ev)
	}
	_ = s.UpsertMessages(ctx, run, []mail.Message{mailtest.Message("src", "1")})
	_ = s.FinishRun(ctx, run, runstate.Succeeded, RunTotals{Seen: 1, Written: 1}, nil)
	if ev := <-runs.C; ev.State != runstate.Succeeded || ev.Totals.Written != 1 || ev.SourceID != "src" {
		t.Fatalf("finish event %+v", ev)
	}
	sources, _ := s.ListSources(ctx)
	folders, _ := s.ListFolders(ctx, "src")
	if len(sources) != 1 || sources[0].CreatedAt != "2026-10-14T09:00:00Z" || len(folders) != 1 || folders[0].Messages != 1 || folders[0].Folder.Role != mail.RoleInbox {
		t.Fatalf("lists: %+v %+v", sources, folders)
	}
	if err := s.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	if size, err := s.SizeBytes(ctx); err != nil || size <= 0 {
		t.Fatalf("size %d %v", size, err)
	}
}

func TestWriteVerdictsRejectsInvalidVerdicts(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	_ = s.UpsertMessages(ctx, 0, []mail.Message{mailtest.Message("src", "1")})
	refs, _ := s.ScopeStores(ctx)
	var row ScopeRow
	_ = s.EachStaleScope(ctx, refs[0], "", 1, func(page []ScopeRow) error { row = page[0]; return nil })
	good := []VerdictRow{{RowID: row.RowID, Verdict: scope.Owner, Reason: scope.ReasonAddressedFlag}}
	if err := s.WriteVerdicts(ctx, "d", good); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []VerdictRow{{RowID: row.RowID, Verdict: "mine", Reason: scope.ReasonAddressedFlag}, {RowID: row.RowID, Verdict: scope.Owner, Reason: "guess"}} {
		if err := s.WriteVerdicts(ctx, "d", []VerdictRow{bad}); err == nil {
			t.Errorf("%+v must be rejected", bad)
		}
	}
	if counts, _ := s.VerdictCounts(ctx); counts[scope.Owner] != 1 {
		t.Fatal("a rejected write must keep the previous verdicts")
	}
}
