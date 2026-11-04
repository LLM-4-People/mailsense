package syncer

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/resource"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// pickedViews reads the snapshot a selection write would have returned, for
// tests that configure the selection directly instead of calling Pick.
func pickedViews(t *testing.T, s *Syncer) []store.MailboxView {
	t.Helper()
	views, err := s.Mailboxes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return views
}

func waitJob(t *testing.T, m *jobs.Manager, id string) jobs.Info {
	t.Helper()
	j, ok := m.Get(id)
	if !ok {
		t.Fatalf("job %s does not exist", id)
	}
	select {
	case <-j.Done():
	case <-time.After(10 * time.Second):
		t.Fatalf("job %s did not finish", id)
	}
	return j.Info()
}

func TestStartPickedSyncsPickedMailboxesAndRecordsTheOwner(t *testing.T) {
	ctx := t.Context()
	s, st, _, cfg := setup(t, map[string]any{source.SettingSelected: []any{}})
	c := &fakeConn{id: "fake", items: map[string][]item{
		"Inbox": items(2, inbox, "i"),
		"Sent":  items(1, sent, "s"),
	}}
	if _, err := s.Discover(ctx, c); err != nil {
		t.Fatal(err)
	}
	views, err := s.Pick(ctx, fakeKey)
	if err != nil {
		t.Fatal(err)
	}
	m := jobs.NewManager(cfg, nil, nil)
	if _, err := s.StartPicked(ctx, m, []source.Connector{c}, views); err != nil {
		t.Fatal(err)
	}
	// The list names the job the pick started. Its status is deliberately not
	// asserted: the job runs on its own goroutine and a fast run can already be
	// finished when this read happens (observed once in the loaded sharded
	// gate), so "running right now" is a property of the scheduler, not of the
	// start. That it ran is proved by waitJob and the owner/count checks below.
	list := m.List()
	if len(list) != 1 || list[0].Kind != JobKind || list[0].Name != "fake" {
		t.Fatalf("jobs after pick: %+v", list)
	}
	if info := waitJob(t, m, list[0].ID); info.Status != runstate.Succeeded {
		t.Fatalf("first sync: %+v", info)
	}
	owners, err := st.Owners(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(owners[fakeKey].Reported, mailtest.Owner) {
		t.Fatalf("owner identity after the first sync: %+v", owners[fakeKey])
	}
	if counts, _ := st.CountByRole(ctx); counts[mail.RoleInbox] != 2 || counts[mail.RoleSent] != 1 {
		t.Fatalf("mail after the first sync: %v", counts)
	}
}

func TestStartPickedWithoutASelectionStartsNothing(t *testing.T) {
	ctx := t.Context()
	s, _, _, cfg := setup(t, map[string]any{source.SettingSelected: []any{}})
	c := &fakeConn{id: "fake", items: map[string][]item{"Inbox": items(1, inbox, "i")}}
	if _, err := s.Discover(ctx, c); err != nil {
		t.Fatal(err)
	}
	m := jobs.NewManager(cfg, nil, nil)
	if _, err := s.StartPicked(ctx, m, []source.Connector{c}, nil); err != nil {
		t.Fatal(err)
	}
	if list := m.List(); len(list) != 0 {
		t.Fatalf("a pick-less start created jobs: %+v", list)
	}
}

func TestStartPickedSkipsASourceAlreadySyncing(t *testing.T) {
	ctx := t.Context()
	s, _, _, cfg := setup(t, nil)
	block, entered := make(chan struct{}), make(chan struct{})
	c := &fakeConn{id: "fake", items: map[string][]item{"Inbox": items(3, inbox, "i")}, block: block, entered: entered}
	if _, err := s.Discover(ctx, c); err != nil {
		t.Fatal(err)
	}
	m := jobs.NewManager(cfg, nil, nil)
	views := pickedViews(t, s)
	if _, err := s.StartPicked(ctx, m, []source.Connector{c}, views); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the first sync did not start")
	}
	if _, err := s.StartPicked(ctx, m, []source.Connector{c}, views); err != nil {
		t.Fatalf("ErrSourceBusy must be benign: %v", err)
	}
	if list := m.List(); len(list) != 1 {
		t.Fatalf("a second job was created: %+v", list)
	}
	close(block)
	waitJob(t, m, m.List()[0].ID)
}

func TestStartPickedStartsOneJobPerSource(t *testing.T) {
	ctx := t.Context()
	s, _, _, cfg := setup(t, nil)
	c1 := &fakeConn{id: "fake", items: map[string][]item{"Inbox": items(1, inbox, "i")}}
	c2 := &fakeConn{id: "fake2", items: map[string][]item{}}
	for _, c := range []source.Connector{c1, c2} {
		if _, err := s.Discover(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	m := jobs.NewManager(cfg, nil, nil)
	if _, err := s.StartPicked(ctx, m, []source.Connector{c1, c2}, pickedViews(t, s)); err != nil {
		t.Fatal(err)
	}
	list := m.List()
	if len(list) != 2 {
		t.Fatalf("jobs per picked source: %+v", list)
	}
	for _, info := range list {
		if got := waitJob(t, m, info.ID); got.Status != runstate.Succeeded {
			t.Fatalf("job %s: %+v", info.ID, got)
		}
	}
}

func TestStartPickedLeavesAnUnpickedSourceOut(t *testing.T) {
	ctx := t.Context()
	s, _, _, cfg := setup(t, map[string]any{source.SettingSelected: []any{fakeKey}})
	c1 := &fakeConn{id: "fake", items: map[string][]item{"Inbox": items(1, inbox, "i")}}
	c2 := &fakeConn{id: "fake2", items: map[string][]item{}}
	for _, c := range []source.Connector{c1, c2} {
		if _, err := s.Discover(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	m := jobs.NewManager(cfg, nil, nil)
	if _, err := s.StartPicked(ctx, m, []source.Connector{c1, c2}, pickedViews(t, s)); err != nil {
		t.Fatal(err)
	}
	list := m.List()
	if len(list) != 1 || list[0].Name != "fake" {
		t.Fatalf("an unpicked source was synced: %+v", list)
	}
	waitJob(t, m, list[0].ID)
}

func TestStartPickedKeepsTheSelectionWhenAJobCannotStart(t *testing.T) {
	ctx := t.Context()
	s, _, _, cfg := setup(t, nil)
	c := &fakeConn{id: "fake", items: map[string][]item{"Inbox": items(1, inbox, "i")}}
	if _, err := s.Discover(ctx, c); err != nil {
		t.Fatal(err)
	}
	m := jobs.NewManager(cfg, nil, nil)
	views := pickedViews(t, s)
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StartPicked(ctx, m, []source.Connector{c}, views); err == nil {
		t.Fatal("a refused job must be reported")
	}
	if got := cfg.Strings(source.SettingSelected); !slices.Contains(got, fakeKey) {
		t.Fatalf("the selection was changed: %v", got)
	}
}

// fixedSampler reports one host state; a test guard needs no platform.
type fixedSampler struct{ usage resource.Usage }

func (f fixedSampler) Sample(context.Context) (resource.Usage, error) { return f.usage, nil }

// TestStartPickedStartsEssentialMailSyncUnderCPUPressure: new mail must keep
// arriving while the host is busy. The picked sync is an essential writer, so
// the CPU and memory ceilings neither refuse nor pause it; the disk ceiling
// still applies (and is below its limit here).
func TestStartPickedStartsEssentialMailSyncUnderCPUPressure(t *testing.T) {
	ctx := t.Context()
	s, _, _, cfg := setup(t, map[string]any{source.SettingSelected: []any{}})
	c := &fakeConn{id: "fake", items: map[string][]item{"Inbox": items(2, inbox, "i")}}
	if _, err := s.Discover(ctx, c); err != nil {
		t.Fatal(err)
	}
	views, err := s.Pick(ctx, fakeKey)
	if err != nil {
		t.Fatal(err)
	}
	known := func(p float64) resource.Reading { return resource.Reading{Percent: p, Known: true} }
	guard := resource.NewGuardWithSampler(cfg, t.TempDir(), nil, fixedSampler{usage: resource.Usage{
		CPU: known(95), RAM: known(95), Disk: known(50),
	}})
	m := jobs.NewManager(cfg, nil, nil, jobs.WithGuard(guard))
	if _, err := s.StartPicked(ctx, m, []source.Connector{c}, views); err != nil {
		t.Fatalf("essential sync refused under CPU pressure: %v", err)
	}
	list := m.List()
	if len(list) != 1 || list[0].Kind != JobKind {
		t.Fatalf("jobs after pick: %+v", list)
	}
	if info := waitJob(t, m, list[0].ID); info.Status != runstate.Succeeded {
		t.Fatalf("essential sync: %+v", info)
	}
}

// TestStartPickedUsesTheSnapshotItIsGiven pins the scan the pick no longer
// pays: the start maps sources from the views the caller passed, not from a
// fresh store read. The store here has no selection, so only the passed
// snapshot can select the source; a re-read would start nothing.
func TestStartPickedUsesTheSnapshotItIsGiven(t *testing.T) {
	ctx := t.Context()
	s, _, _, cfg := setup(t, map[string]any{source.SettingSelected: []any{}})
	c := &fakeConn{id: "fake", items: map[string][]item{"Inbox": items(1, inbox, "i")}}
	if _, err := s.Discover(ctx, c); err != nil {
		t.Fatal(err)
	}
	m := jobs.NewManager(cfg, nil, nil)
	views := []store.MailboxView{{
		Mailbox:  source.Mailbox{Key: fakeKey, SourceID: "fake"},
		Selected: true,
	}}
	if _, err := s.StartPicked(ctx, m, []source.Connector{c}, views); err != nil {
		t.Fatal(err)
	}
	list := m.List()
	if len(list) != 1 || list[0].Name != "fake" {
		t.Fatalf("the passed snapshot did not select the source: %+v", list)
	}
	waitJob(t, m, list[0].ID)
}
