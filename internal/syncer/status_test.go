package syncer

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/fixturetest/fixturewait"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/source"
)

func discoveredFake(t *testing.T, s *Syncer, selected bool) *fakeConn {
	t.Helper()
	ctx := t.Context()
	c := &fakeConn{id: "fake", items: map[string][]item{
		"Inbox": items(2, inbox, "i"),
		"Sent":  items(1, sent, "s"),
	}}
	if _, err := s.Discover(ctx, c); err != nil {
		t.Fatal(err)
	}
	if selected {
		if _, err := s.Pick(ctx, fakeKey); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func TestStatusStartsUnsyncedAndFollowsRunOutcomes(t *testing.T) {
	ctx := t.Context()
	s, _, _, _ := setup(t, map[string]any{source.SettingSelected: []any{}})
	if st := s.Status(); st.Synced || st.Running || len(st.Sources) != 0 {
		t.Fatalf("a boot with no run must not be synced: %+v", st)
	}
	c := discoveredFake(t, s, true)
	if _, err := s.Run(ctx, c, nil); err != nil {
		t.Fatal(err)
	}
	st := s.Status()
	if !st.Synced || st.Running || st.LastSuccess.IsZero() || st.LastError != "" {
		t.Fatalf("after a successful run: %+v", st)
	}
	// A later failed run proves the mailbox may have missed mail: the same
	// status must stop claiming "synced" and keep the failure.
	c.version = 2
	c.failOn = "Inbox"
	if _, err := s.Run(ctx, c, nil); err == nil {
		t.Fatal("the failing run must return an error")
	}
	st = s.Status()
	if st.Synced || st.LastError == "" || !st.LastSuccess.Before(st.LastAttempt) {
		t.Fatalf("after a failed run: %+v", st)
	}
	if st.Partial != true {
		t.Fatalf("a per-folder failure is partial, not a lost source: %+v", st)
	}
}

func TestMailOnlyRunSkipsTheWorldSync(t *testing.T) {
	s, _, _, _ := setup(t, map[string]any{source.SettingSelected: []any{}})
	c := discoveredFake(t, s, true)
	rep, err := s.runFolders(t.Context(), c, nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.World != nil {
		t.Fatalf("a mail-only run read the world model: %+v", rep.World)
	}
	if rep.Totals.Written == 0 {
		t.Fatalf("a mail-only run wrote nothing: %+v", rep.Totals)
	}
	full, err := s.Run(t.Context(), c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if full.World == nil {
		t.Fatal("a full run must still refresh the world model")
	}
}

func TestStatusHasOneAggregateFreshnessField(t *testing.T) {
	// One fact, one name: the whole-mailbox freshness is Synced. A second
	// boolean on Status would be the same fact under another name, while
	// SourceStatus.Fresh stays the per-source fact.
	typ := reflect.TypeOf(Status{})
	var found []string
	for i := 0; i < typ.NumField(); i++ {
		switch name := typ.Field(i).Name; name {
		case "Synced", "Fresh":
			found = append(found, name)
		}
	}
	if !reflect.DeepEqual(found, []string{"Synced"}) {
		t.Fatalf("aggregate freshness fields %v, want only Synced", found)
	}
	if _, ok := reflect.TypeOf(SourceStatus{}).FieldByName("Fresh"); !ok {
		t.Fatal("SourceStatus.Fresh is the per-source fact and must stay")
	}
}

func TestSchedulerFetchesImmediatelyHonorsAutoAndStops(t *testing.T) {
	s, _, _, cfg := setup(t, map[string]any{source.SettingSelected: []any{}})
	c := discoveredFake(t, s, true)
	m := jobs.NewManager(cfg, nil, nil)
	sched := NewScheduler(s, cfg, m, func() []source.Connector { return []source.Connector{c} })

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		sched.Run(ctx)
	}()
	fixturewait.WaitUntil(t, 10*time.Second, "the immediate sync job", func() bool { return len(m.List()) == 1 })
	info := waitJob(t, m, m.List()[0].ID)
	if info.Status != "succeeded" {
		t.Fatalf("automatic sync: %+v", info)
	}
	fixturewait.WaitUntil(t, 10*time.Second, "synced status", func() bool { return s.Status().Synced })

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the scheduler did not join on shutdown")
	}

	// Disabled: the loop stays alive but starts nothing.
	if err := cfg.Set("sync.auto", false); err != nil {
		t.Fatal(err)
	}
	before := len(m.List())
	sched.attempt(t.Context())
	if got := len(m.List()); got != before {
		t.Fatalf("sync.auto=false started %d job(s)", got-before)
	}
}
