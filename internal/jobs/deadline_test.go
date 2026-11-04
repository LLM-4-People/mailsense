package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/resource"
	"github.com/LLM-4-People/mailsense/internal/runstate"
)

// testDeadline is short enough that a blocked body is bounded in a test and
// long enough that a scheduling hiccup cannot expire a body that returns at
// once. The bound is injected through Manager.deadlines: the deadline is wall
// time by design, so a fake clock cannot drive it.
const testDeadline = 40 * time.Millisecond

// shortDeadlines is the test policy: one short bound for every kind that is
// not intrinsically unbounded.
func shortDeadlines(kind string) time.Duration {
	if kind == "sync" {
		return noDeadline
	}
	return testDeadline
}

func deadlineManager(t *testing.T, bus *events.Bus, policy deadlinePolicy) *Manager {
	t.Helper()
	m := NewManager(configtest.New(t, nil), bus, nil)
	m.deadlines = policy
	return m
}

// testPause is a resource pause record for the "a pause never survives the
// terminal state" assertions.
func testPause() *resource.LimitError {
	return &resource.LimitError{Kind: resource.CPU, Percent: 95, Limit: 80}
}

// within fails when the job did not finish inside d; the test body waits on
// the job rather than sleeping for a fixed span, so a bounded job is measured
// by its own Done channel.
func within(t *testing.T, j *Job, d time.Duration) Info {
	t.Helper()
	select {
	case <-j.Done():
		return j.Info()
	case <-time.After(d):
		t.Fatalf("job %s did not stop within %s", j.Info().ID, d)
		return Info{}
	}
}

// TestDeadlineStopsABlockedBody: a producer that never reports progress used
// to hold its context (and its Single slot) until the caller's deadline. The
// per-kind deadline stops it in a terminal state, clears Paused and publishes
// exactly one terminal record.
func TestDeadlineStopsABlockedBody(t *testing.T) {
	bus := events.New()
	sub, err := events.Subscribe(bus, TopicJob, 64)
	if err != nil {
		t.Fatal(err)
	}
	m := deadlineManager(t, bus, shortDeadlines)
	released := make(chan struct{})
	defer close(released)
	j, err := m.Start("evidence", "blocked", func(ctx context.Context, _ func(Progress)) error {
		<-released // ignores ctx on purpose: the manager must still finish it
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// A pause record must never survive the terminal state.
	j.mu.Lock()
	j.info.Paused = testPause()
	j.mu.Unlock()

	info := within(t, j, 2*time.Second)
	if info.Status != runstate.Cancelled {
		t.Fatalf("expired job status = %q, want cancelled", info.Status)
	}
	if info.Paused != nil {
		t.Fatalf("expired job kept a pause record: %+v", info.Paused)
	}
	if !info.FinishedAt.After(info.StartedAt) && !info.FinishedAt.Equal(info.StartedAt) {
		t.Fatalf("expired job has no finish time: %+v", info)
	}
	var deadline *DeadlineError
	if !errors.As(j.Err(), &deadline) || deadline.Kind != "evidence" || deadline.Deadline != testDeadline {
		t.Fatalf("terminal error = %v, want a DeadlineError for evidence/%s", j.Err(), testDeadline)
	}
	if !errors.Is(j.Err(), context.DeadlineExceeded) {
		t.Fatalf("terminal error %v is not a deadline", j.Err())
	}
	if info.Error == "" || info.Error != j.Err().Error() {
		t.Fatalf("wire error %q, want the redacted deadline text", info.Error)
	}

	var events []Info
drain:
	for {
		select {
		case event := <-sub.C:
			events = append(events, event)
		default:
			break drain
		}
	}
	if len(events) < 2 {
		t.Fatalf("published %d records, want the start, the expiry and no more", len(events))
	}
	var terminal []Info
	for i, event := range events {
		if event.Status == runstate.Running {
			if i != 0 {
				t.Fatalf("a running record was published after the start: %+v", events)
			}
			continue
		}
		terminal = append(terminal, event)
	}
	if len(terminal) != 1 || terminal[0].Status != runstate.Cancelled {
		t.Fatalf("terminal publications = %+v, want exactly one cancelled record", terminal)
	}
	if last := events[len(events)-1]; last.Status != runstate.Cancelled || last.FinishedAt.IsZero() {
		t.Fatalf("the last published record is not the terminal one: %+v", events)
	}
}

// TestDeadlineKeepsAPublishedPausedRecordHonest: the pause record set through
// the real path (the resource guard) is cleared by the expiry, and the
// terminal record carries no pause.
func TestDeadlineKeepsAPublishedPausedRecordHonest(t *testing.T) {
	m := deadlineManager(t, nil, shortDeadlines)
	released := make(chan struct{})
	defer close(released)
	j, err := m.Start("triage", "blocked", func(context.Context, func(Progress)) error {
		<-released
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	m.setPaused(j, testPause())
	info := within(t, j, 2*time.Second)
	if info.Paused != nil || info.Status != runstate.Cancelled {
		t.Fatalf("expired job = %+v, want cancelled with no pause", info)
	}
}

// TestDeadlineReleasesTheSingleSlot: a hung run must not keep a Single slot
// forever. The next claim of the slot succeeds and its job runs.
func TestDeadlineReleasesTheSingleSlot(t *testing.T) {
	m := deadlineManager(t, nil, shortDeadlines)
	var s Single
	blocked := make(chan struct{})
	defer close(blocked)
	if !s.Begin() {
		t.Fatal("a free slot can be claimed")
	}
	first, err := StartClaimed(&s, m, "evidence", "blocked", func(context.Context, func(Progress)) error {
		<-blocked
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	within(t, first, 2*time.Second)
	if !s.Begin() {
		t.Fatal("the expired job kept the Single slot")
	}
	s.End()
	if !s.Begin() {
		t.Fatal("the released slot cannot be claimed again")
	}
	second, err := StartClaimed(&s, m, "evidence", "next", func(context.Context, func(Progress)) error {
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if info := within(t, second, 2*time.Second); info.Status != runstate.Succeeded {
		t.Fatalf("the run after an expiry = %+v, want success", info)
	}
}

// TestStartEssentialHasNoDeadline: new mail may take as long as the mailbox
// needs. A sync body that outlives every bounded kind keeps running until it
// is cancelled, and the policy table itself says so.
func TestStartEssentialHasNoDeadline(t *testing.T) {
	if got := defaultDeadlinePolicy("sync"); got != noDeadline {
		t.Fatalf("the shipped sync policy is %s, want unbounded", got)
	}
	m := deadlineManager(t, nil, func(string) time.Duration { return testDeadline })
	started := make(chan struct{})
	j, err := m.StartEssential("sync", "mailbox", func(ctx context.Context, _ func(Progress)) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	select {
	case <-j.Done():
		t.Fatalf("an essential job was bounded by the kind policy: %+v", j.Info())
	case <-time.After(4 * testDeadline):
	}
	if info := j.Info(); info.Status != runstate.Running {
		t.Fatalf("an essential job left running on its own: %+v", info)
	}
	j.Cancel()
	if info := within(t, j, 2*time.Second); info.Status != runstate.Cancelled {
		t.Fatalf("a cancelled essential job = %+v", info)
	}
}

// TestUnknownKindUsesTheDefaultBound: a producer the table does not name is
// bounded by the fallback, never left to hang.
func TestUnknownKindUsesTheDefaultBound(t *testing.T) {
	for kind, want := range kindDeadlines {
		if got := defaultDeadlinePolicy(kind); got != want {
			t.Fatalf("policy(%q) = %s, want %s", kind, got, want)
		}
	}
	// Every bounded kind must actually be bounded; only the documented
	// unbounded kinds may be 0.
	for kind, bound := range kindDeadlines {
		if bound < 0 {
			t.Fatalf("policy(%q) = %s, want a positive bound or noDeadline", kind, bound)
		}
	}
	if got := defaultDeadlinePolicy("a.kind.nobody.declared"); got != fallbackDeadline || got <= 0 {
		t.Fatalf("unknown kind bound = %s, want the %s fallback", got, fallbackDeadline)
	}

	m := NewManager(configtest.New(t, nil), nil, nil)
	if got := m.deadlineFor("a.kind.nobody.declared"); got != fallbackDeadline {
		t.Fatalf("Manager bound for an unknown kind = %s, want %s", got, fallbackDeadline)
	}
	// A Manager built as a literal (no policy set) resolves the shipped
	// table instead of leaving every job unbounded.
	literal := &Manager{}
	if literal.deadlineFor("evidence") != kindDeadlines["evidence"] || literal.deadlineFor("unknown") != fallbackDeadline {
		t.Fatal("a nil policy must fall back to the shipped table, not to no bound")
	}

	released := make(chan struct{})
	defer close(released)
	j, err := deadlineManager(t, nil, shortDeadlines).Start("a.kind.nobody.declared", "new", func(ctx context.Context, _ func(Progress)) error {
		<-released
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	info := within(t, j, 2*time.Second)
	if info.Status != runstate.Cancelled {
		t.Fatalf("an unknown kind was not bounded: %+v", info)
	}
	var deadline *DeadlineError
	if !errors.As(j.Err(), &deadline) || deadline.Kind != "a.kind.nobody.declared" {
		t.Fatalf("unknown-kind error = %v", j.Err())
	}
}

// TestDeadlineErrorCarriesNoContentAndNamesTheBound: the terminal error is
// built from the kind and two durations only, so no mail value can reach the
// job record through it.
func TestDeadlineErrorCarriesNoContentAndNamesTheBound(t *testing.T) {
	err := error(&DeadlineError{Kind: "labels.judge", Deadline: 30 * time.Minute, Elapsed: 31 * time.Minute})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("%v is not a deadline", err)
	}
	want := "jobs: labels.judge job exceeded its 30m0s deadline after 31m0s"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

// TestShutdownJoinsACooperativeExpiredBody: the deadline finalizes the record
// and releases the slot, but Shutdown's join still means the body returned.
// Every production body honors its context, so the join is prompt.
func TestShutdownJoinsACooperativeExpiredBody(t *testing.T) {
	m := deadlineManager(t, nil, shortDeadlines)
	started := make(chan struct{})
	j, err := m.Start("evidence", "cooperative", func(ctx context.Context, _ func(Progress)) error {
		close(started)
		<-ctx.Done() // honors the deadline cancellation
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	info := within(t, j, 2*time.Second)
	if info.Status != runstate.Cancelled {
		t.Fatalf("cooperative expiry = %+v", info)
	}
	joined := make(chan error, 1)
	go func() { joined <- m.Shutdown(t.Context()) }()
	select {
	case err := <-joined:
		if err != nil {
			t.Fatalf("shutdown joined a cooperative expired body: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not join the cooperative expired body")
	}
}

// TestShutdownStillJoinsAfterADeadline: an expiry must not break the
// shutdown join, and a cancelled essential job must still be joined.
func TestShutdownStillJoinsAfterADeadline(t *testing.T) {
	m := deadlineManager(t, nil, shortDeadlines)
	released := make(chan struct{})
	bounded, err := m.Start("index", "bounded", func(ctx context.Context, _ func(Progress)) error {
		<-released // ignores ctx: the deadline, not the body, ends it
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	essential, err := m.StartEssential("sync", "mailbox", func(ctx context.Context, _ func(Progress)) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	within(t, bounded, 2*time.Second)
	close(released)
	if err := m.Shutdown(t.Context()); err != nil {
		t.Fatalf("shutdown after a deadline: %v", err)
	}
	if info := essential.Info(); info.Status != runstate.Cancelled {
		t.Fatalf("the essential job after shutdown = %+v", info)
	}
}

// TestDeadlineDoesNotTouchASuccessfulJob: a job that finishes inside its
// bound is a success and its error stays nil, so the deadline cannot turn
// ordinary work into a failure.
func TestDeadlineDoesNotTouchASuccessfulJob(t *testing.T) {
	m := deadlineManager(t, nil, shortDeadlines)
	j, err := m.Start("evidence", "quick", func(context.Context, func(Progress)) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	info := within(t, j, 2*time.Second)
	if info.Status != runstate.Succeeded || info.Error != "" || j.Err() != nil {
		t.Fatalf("quick job = %+v err=%v, want an untouched success", info, j.Err())
	}
}
