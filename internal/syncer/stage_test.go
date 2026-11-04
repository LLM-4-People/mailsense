package syncer

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

func TestMailboxAndSyncStages(t *testing.T) {
	s, st, _, cfg := setup(t, map[string]any{source.SettingSelected: []any{}})
	mb, sy := MailboxesStage(cfg, st), SyncStage(cfg, st)
	status := func(stage learning.Stage) learning.StageStatus {
		t.Helper()
		out, err := stage.Status(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := status(mb); got.State != learning.Blocked || got.Blockers[0].Code != BlockNoSelection {
		t.Fatalf("nothing picked: %+v", got)
	}
	if got := status(sy); got.State != learning.Pending {
		t.Fatalf("sync waits for a pick: %+v", got)
	}
	// Picked (settings.json) but never discovered: not found (waited on,
	// never replaced; the Pick action refuses such a key).
	if err := cfg.Set(source.SettingSelected, []any{fakeKey}); err != nil {
		t.Fatal(err)
	}
	if got := status(mb); got.State != learning.Blocked || got.Blockers[0].Code != BlockNotFound || got.Blockers[0].Subject != fakeKey {
		t.Fatalf("not discovered: %+v", got)
	}
	// Discovered, no owner reported: blocked until declared.
	c := &fakeConn{id: "fake", noOwner: true, items: map[string][]item{"Inbox": items(3, inbox, "i"), "Sent": items(2, sent, "s")}}
	if _, err := s.Run(t.Context(), c, nil); err != nil {
		t.Fatal(err)
	}
	if got := status(mb); got.State != learning.Blocked || got.Blockers[0].Code != BlockNoIdentity {
		t.Fatalf("no identity: %+v", got)
	}
	if got := status(sy); got.State != learning.Running || got.Progress.Done != 0 || got.Progress.Total != 5 {
		t.Fatalf("stored but not judged: %+v", got)
	}
	// Reported owner: both done.
	c.noOwner = false
	c.version++
	if _, err := s.Run(t.Context(), c, nil); err != nil {
		t.Fatal(err)
	}
	if got := status(mb); got.State != learning.Done || got.Progress.Done != 1 {
		t.Fatalf("mailboxes: %+v", got)
	}
	if got := status(sy); got.State != learning.Done || got.Progress.Done != 5 || got.Progress.Total != 5 {
		t.Fatalf("sync: %+v", got)
	}
	// An unreadable picked mailbox is reported, never replaced.
	boxes := c.Mailboxes()
	boxes[0].Error = "0x80040115"
	if err := st.SaveMailboxes(t.Context(), "fake", boxes); err != nil {
		t.Fatal(err)
	}
	if got := status(mb); got.State != learning.Blocked || got.Blockers[0].Code != BlockUnreadable {
		t.Fatalf("unreadable: %+v", got)
	}
}

// The scope pass pages its stale scan and closes each page before writing
// it: with a single pooled connection the write would otherwise wait for the
// connection the read cursor holds until the context ends.
func TestScopePassRunsWithOneConnection(t *testing.T) {
	s, st, _, _ := setup(t, map[string]any{"store.max_open_conns": 1, "sync.batch_size": 2,
		source.SettingSelected: []any{fakeKey}})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	if err := st.UpsertSource(ctx, "fake", "fake", "Fake", source.Capabilities{Incremental: true, OwnerIdentity: true}); err != nil {
		t.Fatal(err)
	}
	var batch []mail.Message
	for i := range 5 {
		batch = append(batch, mailtest.Message("fake", fmt.Sprintf("scope-%d", i)))
	}
	if err := st.UpsertMessages(ctx, 0, batch); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveReported(ctx, store.StoreRef{SourceID: "fake", StoreID: "store-1"}, []string{mailtest.Owner}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecomputeScope(ctx); err != nil {
		t.Fatalf("scope pass with store.max_open_conns=1: %v", err)
	}
	if counts, err := st.VerdictCounts(ctx); err != nil || counts[scope.Owner] != 5 {
		t.Fatalf("verdicts after the pass: %v %v", counts, err)
	}
}

// A store whose owner identity changed still owes scope work, so the sync
// stage is not Done even though every stored verdict has a revision.
func TestSyncStageNotDoneWhenScopeIdentityChanged(t *testing.T) {
	s, st, _, cfg := setup(t, map[string]any{source.SettingSelected: []any{fakeKey}})
	ctx := t.Context()
	c := &fakeConn{id: "fake", items: map[string][]item{"Inbox": items(3, inbox, "i"), "Sent": items(2, sent, "s")}}
	if _, err := s.Run(ctx, c, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := SyncStage(cfg, st).Status(ctx); err != nil || got.State != learning.Done || got.Progress.Done != 5 {
		t.Fatalf("judged under the reported identity: %+v %v", got, err)
	}

	refs, err := st.ScopeStores(ctx)
	if err != nil || len(refs) != 1 {
		t.Fatalf("scope stores: %+v %v", refs, err)
	}
	if err := st.SaveReported(ctx, refs[0], []string{"other@example.com"}); err != nil {
		t.Fatal(err)
	}
	got, err := SyncStage(cfg, st).Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.State == learning.Done || got.Progress.Done != 0 || got.Progress.Total != 5 {
		t.Fatalf("scope owes a re-judge: %+v", got)
	}
}

// #9: layers ask capabilities: an addressed-to-me value from a source
// that has no such flag is ignored (fakeConn reports Incremental only).
func TestScopeIgnoresEvidenceTheSourceCannotReport(t *testing.T) {
	s, _, _, _ := setup(t, nil)
	m := mailtest.Message("fake", "x")
	m.AddressedToMe, m.Recipients = "bcc", []mail.Recipient{{Kind: mail.To, Address: "team@example.com"}}
	rep, err := s.Run(t.Context(), &fakeConn{id: "fake", items: map[string][]item{"Inbox": {{msg: m}}}}, nil)
	if err != nil || rep.Scope.Counts[scope.Shared] != 1 || rep.Scope.Counts[scope.Owner] != 0 {
		t.Fatalf("the flag is not evidence here: %+v %v", rep.Scope, err)
	}
}
