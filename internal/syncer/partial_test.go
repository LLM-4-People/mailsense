package syncer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// A folder that fails while another writes is partial success: the run stays
// failed (the failed folder is named in Error and recorded), but the mail that
// arrived is safe to follow.
func TestFolderErrorIsPartialSuccess(t *testing.T) {
	s, st, bus, _ := setup(t, nil)
	finished, err := events.Subscribe(bus, TopicFinished, 4)
	if err != nil {
		t.Fatal(err)
	}
	c := &fakeConn{id: "fake", items: map[string][]item{
		"Inbox": items(2, inbox, "i"),
		"Sent":  items(2, sent, "s"),
	}, failOn: "Inbox"}
	rep, err := s.Run(t.Context(), c, nil)
	if err == nil || !strings.Contains(err.Error(), "folder Inbox") || !strings.Contains(rep.Error, "folder Inbox") {
		t.Fatalf("the failed folder must still be named: %v / %q", err, rep.Error)
	}
	if rep.Status != runstate.Failed || !rep.Partial || !rep.Advanced() {
		t.Fatalf("folder error: status %s partial %v advanced %v", rep.Status, rep.Partial, rep.Advanced())
	}
	if rep.Totals.Written != 2 {
		t.Fatalf("the successful folder's mail must be written: %+v", rep.Totals)
	}
	if counts, _ := st.CountByRole(t.Context()); counts[mail.RoleSent] != 2 {
		t.Fatalf("the successful folder's mail is stored: %v", counts)
	}
	runs, err := st.ListRuns(t.Context(), 1)
	if err != nil || runs[0].State != runstate.Failed || runs[0].Error == "" {
		t.Fatalf("the run record keeps the failure: %+v %v", runs, err)
	}
	if ev := <-finished.C; ev.RunID != rep.RunID || !ev.Partial || !ev.Advanced() {
		t.Fatalf("the finished event must carry the partial success: %+v", ev)
	}
}

// An output failure (the store cannot write) ends the run even after folders
// wrote: it is not partial and nothing downstream may follow it.
func TestOutputFailureIsNotPartial(t *testing.T) {
	s, _, _, cfg := setup(t, nil)
	ctx := t.Context()
	c := &fakeConn{id: "fake", items: map[string][]item{"Inbox": items(1, inbox, "i"), "Sent": items(1, sent, "s")}}
	if _, err := s.Run(ctx, c, nil); err != nil {
		t.Fatal(err)
	}
	raw := openRawStore(t, store.Path(cfg))
	if _, err := raw.ExecContext(ctx, `CREATE TRIGGER fail_cursor BEFORE INSERT ON sync_cursors
		BEGIN SELECT RAISE(ABORT, 'cursor blocked'); END`); err != nil {
		t.Fatal(err)
	}
	c.version = 1
	c.items["Inbox"] = items(2, inbox, "j") // a write that must not advance
	rep, err := s.Run(ctx, c, nil)
	if err == nil || !isOutput(err) {
		t.Fatalf("the cursor write must be an output failure: %v", err)
	}
	if rep.Status != runstate.Failed || rep.Partial || rep.Advanced() {
		t.Fatalf("output failure: status %s partial %v advanced %v (written %d)",
			rep.Status, rep.Partial, rep.Advanced(), rep.Totals.Written)
	}
	if rep.Totals.Written == 0 {
		t.Fatal("the fixture must write before the output failure")
	}
}

// An unavailable source aborts after a complete folder already wrote: the run
// is not partial, so its mail is not followed.
func TestUnavailableSourceIsNotPartial(t *testing.T) {
	s, _, _, _ := setup(t, nil)
	folders := []mail.Folder{
		{SourceID: "fake", StoreID: "store-1", StoreName: "Amélie", StoreKind: mail.StorePrimary, Path: "Sent", Role: mail.RoleSent, ItemCount: -1},
		{SourceID: "fake", StoreID: "store-1", StoreName: "Amélie", StoreKind: mail.StorePrimary, Path: "Inbox", Role: mail.RoleInbox, ItemCount: -1},
	}
	c := &fakeConn{id: "fake", folders: folders, items: map[string][]item{
		"Sent":  items(1, sent, "s"),
		"Inbox": items(1, inbox, "i"),
	}, failOn: "Inbox", failErr: fmt.Errorf("outlook: GetArray: %w", source.Unavailable("Outlook is gone"))}
	rep, err := s.Run(t.Context(), c, nil)
	if !errors.Is(err, source.ErrUnavailable) {
		t.Fatalf("want unavailable, got %v", err)
	}
	if rep.Status != runstate.Failed || rep.Partial || rep.Advanced() || rep.Totals.Written == 0 {
		t.Fatalf("unavailable: status %s partial %v advanced %v (written %d)",
			rep.Status, rep.Partial, rep.Advanced(), rep.Totals.Written)
	}
}

// A cancelled run is never partial, even when a folder already wrote.
func TestCancelledRunIsNotPartial(t *testing.T) {
	s, _, _, _ := setup(t, nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	block, entered := make(chan struct{}), make(chan struct{})
	c := &fakeConn{id: "fake", items: map[string][]item{"Inbox": items(5, inbox, "i")}, block: block, entered: entered}
	type outcome struct {
		rep Report
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		rep, err := s.Run(ctx, c, nil)
		done <- outcome{rep, err}
	}()
	<-entered
	cancel()
	close(block)
	got := <-done
	if got.rep.Status != runstate.Cancelled || got.rep.Partial || got.rep.Advanced() {
		t.Fatalf("cancelled run: status %s partial %v advanced %v", got.rep.Status, got.rep.Partial, got.rep.Advanced())
	}
}

// A successful write advances; the unchanged run that follows is a no-op and
// does not.
func TestSuccessfulWriteAdvancesAndNoOpDoesNot(t *testing.T) {
	s, _, _, _ := setup(t, nil)
	c := &fakeConn{id: "fake", items: map[string][]item{"Inbox": items(1, inbox, "i")}}
	first, err := s.Run(t.Context(), c, nil)
	if err != nil || first.Status != runstate.Succeeded || first.Partial || !first.Advanced() {
		t.Fatalf("first run: %v %+v", err, first)
	}
	again, err := s.Run(t.Context(), c, nil)
	if err != nil || again.Status != runstate.Succeeded || again.Partial || again.Advanced() || again.Totals.Seen != 0 {
		t.Fatalf("unchanged run: %v %+v", err, again)
	}
}

// A held removal is not a change: the message is still stored, so no
// downstream pass follows it.
func TestHeldRemovalDoesNotAdvance(t *testing.T) {
	s, _, _, _ := setup(t, nil)
	c := &fakeConn{id: "fake", items: map[string][]item{"Inbox": items(1, inbox, "i")}}
	if rep, err := s.Run(t.Context(), c, nil); err != nil || !rep.Advanced() {
		t.Fatalf("seed run: %v %+v", err, rep)
	}
	c.version = 1
	c.items["Inbox"] = nil // the message with a Message-ID is held one run
	rep, err := s.Run(t.Context(), c, nil)
	if err != nil || rep.Removed != (store.Removal{Held: 1}) || rep.Totals.Written != 0 {
		t.Fatalf("held removal: %v %+v", err, rep)
	}
	if rep.Scope == nil || rep.Scope.Judged() != 0 {
		t.Fatalf("nothing was rejudged: %+v", rep.Scope)
	}
	if rep.Advanced() {
		t.Fatalf("a held removal is not a change: %+v", rep)
	}
}

// The one advance predicate: no-op and non-partial failed/cancelled runs do
// not advance; a write, a real removal or move, a scope recompute or a
// completed detail read does.
func TestAdvancedPredicate(t *testing.T) {
	cases := []struct {
		name string
		rep  Report
		want bool
	}{
		{"failed without the partial flag", Report{Status: runstate.Failed, Totals: store.RunTotals{Written: 3}}, false},
		{"partial with a write", Report{Status: runstate.Failed, Partial: true, Totals: store.RunTotals{Written: 3}}, true},
		{"partial with no change", Report{Status: runstate.Failed, Partial: true}, false},
		{"cancelled with a write", Report{Status: runstate.Cancelled, Totals: store.RunTotals{Written: 3}}, false},
		{"succeeded no-op", Report{Status: runstate.Succeeded}, false},
		{"succeeded write", Report{Status: runstate.Succeeded, Totals: store.RunTotals{Written: 1}}, true},
		{"succeeded deletion", Report{Status: runstate.Succeeded, Removed: store.Removal{Deleted: 1}}, true},
		{"succeeded move", Report{Status: runstate.Succeeded, Removed: store.Removal{Moved: 1}}, true},
		{"succeeded held removal", Report{Status: runstate.Succeeded, Removed: store.Removal{Held: 2}}, false},
		{"succeeded scope recompute", Report{Status: runstate.Succeeded, Scope: &ScopeReport{Stores: []StoreScope{{Judged: 2}}}}, true},
		{"succeeded scope with nothing judged", Report{Status: runstate.Succeeded, Scope: &ScopeReport{}}, false},
		{"succeeded details completed", Report{Status: runstate.Succeeded, Details: &DetailReport{Completed: 1}}, true},
		{"succeeded details pending only", Report{Status: runstate.Succeeded, Details: &DetailReport{Pending: 1}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.rep.Advanced(); got != c.want {
				t.Fatalf("Advanced() = %v, want %v (%+v)", got, c.want, c.rep)
			}
		})
	}
}
