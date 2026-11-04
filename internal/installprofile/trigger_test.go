package installprofile

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/syncer"
)

var testStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func advancedSync() syncer.Report {
	return syncer.Report{Status: runstate.Succeeded, Totals: store.RunTotals{Written: 1}}
}

func waitFor[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// startTrigger starts the worker over a bus. The cleanup stops it, so a
// failing test cannot leak a goroutine.
func startTrigger(t *testing.T, l *Learner, clk *clocktest.Fake, run func(context.Context) (Report, error)) (*trigger, *events.Bus, func()) {
	t.Helper()
	tr := newTrigger(l)
	tr.clock = clk
	if run != nil {
		tr.run = run
	}
	bus := events.New()
	t.Cleanup(bus.Close)
	stop, done, err := tr.watch(bus)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stop(); <-done })
	return tr, bus, stop
}

// A burst of advanced syncs is one request and one pass: the settle window
// collects the burst before the worker starts.
func TestBurstOfAdvancedSyncsProducesOneRun(t *testing.T) {
	clk := clocktest.New(testStart)
	settled, release := make(chan struct{}), make(chan struct{})
	var hold sync.Once
	clk.OnSleep = func(ctx context.Context, d time.Duration) error {
		blocked := false
		if d == learnSettle {
			hold.Do(func() {
				blocked = true
				close(settled)
			})
		}
		if blocked {
			select {
			case <-release:
				clk.Advance(d)
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		clk.Advance(d)
		return ctx.Err()
	}
	starts := make(chan struct{}, 8)
	var runs atomic.Int64
	run := func(context.Context) (Report, error) {
		runs.Add(1)
		starts <- struct{}{}
		return Report{}, nil
	}
	tr, bus, _ := startTrigger(t, New(configtest.New(t, nil), nil, clk), clk, run)

	events.Publish(bus, syncer.TopicFinished, advancedSync())
	waitFor(t, settled, "the coalescing window")
	for range 9 {
		events.Publish(bus, syncer.TopicFinished, advancedSync())
	}
	waitUntil(t, "ten requests at the worker", func() bool { return tr.requested.Load() == 10 })
	close(release)
	waitFor(t, starts, "the learning pass")

	if got := len(tr.requests); got != 0 {
		t.Fatalf("%d coalesced request(s) survived the pass", got)
	}
	if got := runs.Load(); got != 1 {
		t.Fatalf("ten sync events ran the learner %d times, want 1", got)
	}
}

// A sync that arrives while a pass runs is one follow-up, and it waits for
// the interval's remainder before running.
func TestEventDuringRunProducesOneFollowUpAfterTheInterval(t *testing.T) {
	clk := clocktest.New(testStart)
	starts := make(chan time.Time, 8)
	first, release := make(chan struct{}), make(chan struct{})
	var runs atomic.Int64
	run := func(context.Context) (Report, error) {
		n := runs.Add(1)
		starts <- clk.Now()
		if n == 1 {
			close(first)
			<-release
		}
		return Report{}, nil
	}
	tr, bus, _ := startTrigger(t, New(configtest.New(t, nil), nil, clk), clk, run)

	events.Publish(bus, syncer.TopicFinished, advancedSync())
	firstStart := waitFor(t, starts, "the first pass")
	waitFor(t, first, "the first pass to hold")
	for range 10 {
		events.Publish(bus, syncer.TopicFinished, advancedSync())
	}
	waitUntil(t, "the in-flight requests", func() bool { return tr.requested.Load() == 11 })
	close(release)
	secondStart := waitFor(t, starts, "the follow-up pass")

	if got := secondStart.Sub(firstStart); got < minLearnInterval {
		t.Fatalf("the follow-up ran after %s, want at least %s", got, minLearnInterval)
	}
	if got := runs.Load(); got != 2 {
		t.Fatalf("a burst during one pass ran the learner %d times, want 2 (the pass and one follow-up)", got)
	}
	if got := len(tr.requests); got != 0 {
		t.Fatalf("%d request(s) queued beyond the one follow-up", got)
	}
}

// The install profile follows the mail that changed: a partial sync (some
// folders failed) still learns from what arrived, while an output failure and
// a no-op run do not. Negative cases also prove no request reaches the worker.
func TestLearnersFollowAdvancedSyncs(t *testing.T) {
	cases := []struct {
		name string
		rep  syncer.Report
		want bool
	}{
		{"partial with a write", syncer.Report{Status: runstate.Failed, Partial: true, Totals: store.RunTotals{Written: 1}}, true},
		{"output failure with a write", syncer.Report{Status: runstate.Failed, Totals: store.RunTotals{Written: 1}}, false},
		{"successful no-op", syncer.Report{Status: runstate.Succeeded}, false},
		{"successful write", syncer.Report{Status: runstate.Succeeded, Totals: store.RunTotals{Written: 1}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			if got := len(w.cfg.Learned()); got != 0 {
				t.Fatalf("the fixture already learned %d value(s)", got)
			}
			clk := clocktest.New(testStart)
			l := New(w.cfg, w.st, clk)
			done := make(chan struct{}, 1)
			tr, bus, stop := startTrigger(t, l, clk, func(ctx context.Context) (Report, error) {
				rep, err := l.Run(ctx)
				done <- struct{}{}
				return rep, err
			})

			events.Publish(bus, syncer.TopicFinished, c.rep)
			if c.want {
				waitFor(t, done, "the learners after an advanced sync")
			}
			stop()

			if !c.want {
				if got := tr.requested.Load(); got != 0 {
					t.Fatalf("a sync that did not advance made %d request(s)", got)
				}
			}
			got := len(w.cfg.Learned())
			if c.want && got == 0 {
				t.Fatal("a sync that advanced the mail did not run the learners")
			}
			if !c.want && got != 0 {
				t.Fatalf("a sync that did not advance ran the learners (%d learned values)", got)
			}
		})
	}
}

// stop cancels a pass in flight, unsubscribes and joins every goroutine of
// the trigger.
func TestStopCancelsInFlightPassAndJoins(t *testing.T) {
	before := runtime.NumGoroutine()
	for range 3 {
		clk := clocktest.New(testStart)
		started, canceled := make(chan struct{}), make(chan struct{})
		tr := newTrigger(New(configtest.New(t, nil), nil, clk))
		tr.clock = clk
		tr.run = func(ctx context.Context) (Report, error) {
			close(started)
			<-ctx.Done()
			close(canceled)
			return Report{}, ctx.Err()
		}
		bus := events.New()
		stop, _, err := tr.watch(bus)
		if err != nil {
			t.Fatal(err)
		}
		events.Publish(bus, syncer.TopicFinished, advancedSync())
		waitFor(t, started, "the pass to start")

		stopped := make(chan struct{})
		go func() {
			stop()
			close(stopped)
		}()
		waitFor(t, stopped, "stop to return")
		// stop cancels without waiting by contract: the cancellation reaches
		// the pass and the worker exits just after. The caller joins done.
		waitFor(t, canceled, "stop to cancel the pass in flight")
		waitFor(t, tr.done, "the worker to exit")
		bus.Close()
	}
	waitUntil(t, "the trigger goroutines to exit", func() bool { return runtime.NumGoroutine() <= before+2 })
}

type pauseAdmission struct {
	mu     sync.Mutex
	paused bool
	calls  int
}

var errPaused = errors.New("resource: host limit reached")

func (p *pauseAdmission) Admit(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.paused {
		return errPaused
	}
	return nil
}

func (p *pauseAdmission) set(paused bool) {
	p.mu.Lock()
	p.paused = paused
	p.mu.Unlock()
}

func (p *pauseAdmission) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// A paused admission runs nothing and leaves the request pending; the next
// admitted tick learns. The fake clock blocks on the retry wait, so the
// worker cannot spin through the pause.
func TestPausedAdmissionKeepsRequestPending(t *testing.T) {
	w := newWorld(t)
	clk := clocktest.New(testStart)
	retrying, release := make(chan struct{}), make(chan struct{})
	var hold sync.Once
	clk.OnSleep = func(ctx context.Context, d time.Duration) error {
		blocked := false
		if d == admitRetry {
			hold.Do(func() {
				blocked = true
				close(retrying)
			})
		}
		if blocked {
			select {
			case <-release:
				clk.Advance(d)
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		clk.Advance(d)
		return ctx.Err()
	}
	adm := &pauseAdmission{paused: true}
	sink := newLogSink()
	l := New(w.cfg, w.st, clk)
	l.log = slog.New(sink)
	l.SetAdmission(adm)
	starts := make(chan struct{}, 2)
	tr, bus, _ := startTrigger(t, l, clk, func(ctx context.Context) (Report, error) {
		rep, err := l.Run(ctx)
		starts <- struct{}{}
		return rep, err
	})

	events.Publish(bus, syncer.TopicFinished, advancedSync())
	waitFor(t, retrying, "the bounded retry wait")
	if got := len(starts); got != 0 {
		t.Fatalf("a paused admission started %d pass(es)", got)
	}
	if got := len(w.cfg.Learned()); got != 0 {
		t.Fatalf("a paused admission learned %d value(s)", got)
	}
	if adm.count() == 0 {
		t.Fatal("the trigger did not ask admission")
	}
	line := sink.wait(t, "install profile learning paused")
	if line.level != slog.LevelInfo {
		t.Fatalf("the pause log level is %s, want info", line.level)
	}

	adm.set(false)
	close(release)
	waitFor(t, starts, "the next admitted pass")
	if got := len(w.cfg.Learned()); got == 0 {
		t.Fatal("the admitted follow-up did not learn")
	}
	if got := tr.requested.Load(); got != 1 {
		t.Fatalf("the pause produced %d request(s), want the original one", got)
	}
}

// A pass that exceeds its timeout returns, logs once and leaves the previous
// learned values untouched.
func TestTimedOutPassKeepsLearnedValues(t *testing.T) {
	w := newWorld(t)
	clk := clocktest.New(testStart)
	sink := newLogSink()
	l := New(w.cfg, w.st, clk)
	l.log = slog.New(sink)
	if _, err := l.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := w.cfg.Learned()
	if len(before) == 0 {
		t.Fatal("the fixture did not learn its first profile")
	}
	started := make(chan struct{}, 1)
	tr := newTrigger(l)
	tr.clock = clk
	tr.timeout = 25 * time.Millisecond
	tr.run = func(ctx context.Context) (Report, error) {
		started <- struct{}{}
		<-ctx.Done()
		return Report{}, ctx.Err()
	}
	bus := events.New()
	t.Cleanup(bus.Close)
	stop, done, err := tr.watch(bus)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stop(); <-done })

	events.Publish(bus, syncer.TopicFinished, advancedSync())
	waitFor(t, started, "the timed-out pass")

	line := sink.wait(t, "install profile learning timed out")
	if line.level != slog.LevelWarn {
		t.Fatalf("the timeout log level is %s, want warn", line.level)
	}
	if got := w.cfg.Learned(); !reflect.DeepEqual(got, before) {
		t.Fatalf("a timed-out pass changed the learned values:\n got %+v\nwant %+v", got, before)
	}
	stop()
	if got := sink.count("install profile learning timed out"); got != 1 {
		t.Fatalf("the timeout logged %d lines, want 1", got)
	}
}

// The one success line keeps its measured fields and gains the pass duration.
func TestLearnerRunLogsDurationAndMeasurements(t *testing.T) {
	w := newWorld(t)
	sink := newLogSink()
	l := New(w.cfg, w.st, nil)
	l.log = slog.New(sink)
	if _, err := l.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	line := sink.wait(t, "install profile learned")
	for _, key := range []string{"duration", "messages", "shared_domains", "reply_prefixes"} {
		if _, ok := line.attrs[key]; !ok {
			t.Fatalf("the learned line lacks %q: %+v", key, line.attrs)
		}
	}
}

type logLine struct {
	level slog.Level
	msg   string
	attrs map[string]string
}

// logSink records the learner logs without touching the process logger.
type logSink struct {
	mu    sync.Mutex
	lines []logLine
	wake  chan struct{}
}

func newLogSink() *logSink { return &logSink{wake: make(chan struct{}, 1)} }

func (s *logSink) Enabled(context.Context, slog.Level) bool { return true }

func (s *logSink) Handle(_ context.Context, rec slog.Record) error {
	line := logLine{level: rec.Level, msg: rec.Message, attrs: map[string]string{}}
	rec.Attrs(func(a slog.Attr) bool {
		line.attrs[a.Key] = a.Value.String()
		return true
	})
	s.mu.Lock()
	s.lines = append(s.lines, line)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return nil
}

func (s *logSink) WithAttrs([]slog.Attr) slog.Handler { return s }
func (s *logSink) WithGroup(string) slog.Handler      { return s }

func (s *logSink) find(msg string) (logLine, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, line := range s.lines {
		if line.msg == msg {
			return line, true
		}
	}
	return logLine{}, false
}

func (s *logSink) count(msg string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, line := range s.lines {
		if line.msg == msg {
			n++
		}
	}
	return n
}

func (s *logSink) wait(t *testing.T, msg string) logLine {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if line, ok := s.find(msg); ok {
			return line
		}
		select {
		case <-s.wake:
		case <-deadline:
			t.Fatalf("timed out waiting for the %q log", msg)
		}
	}
}
