package outlook

import (
	"context"
	"errors"
	"math"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/source"
)

// Review of the crash-safety round, #2: the final Release of an object is
// a call into Outlook's thread (IRemUnknown::RemRelease) — about nine per
// detail read — yet it was neither paced nor counted (failed first: no
// Release member, rate ≈ 60 % above the setting).
func TestReleasesArePacedAndCounted(t *testing.T) {
	o := profile()
	c, clk := testConnector(t, o, map[string]any{"outlook.pace.busy_share": 1, "outlook.pace.calls_per_second": 50})
	start := clk.Now()
	if _, err := c.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	wall := clk.Now().Sub(start)
	st := c.Stats()
	releases := int64(0)
	for k, m := range st.Members {
		if strings.HasSuffix(k, ".Release") {
			releases += m.Calls
		}
	}
	if releases == 0 || st.Calls != int64(len(o.calls))+releases {
		t.Fatalf("releases %d, calls %d, member calls %d: every release is a counted call", releases, st.Calls, len(o.calls))
	}
	if rate := float64(st.Calls-1) / wall.Seconds(); rate > 50 {
		t.Fatalf("%d calls (releases included) in %s: %.1f calls/s, want ≤ 50", st.Calls, wall, rate)
	}
}

// #7: resume_factor^losses scaled the limits toward zero and the wait
// overflowed time.Duration (a negative wait: no pacing at all). The wait
// is computed in float and capped by outlook.pace.max_wait (failed first).
func TestATinyPaceNeverOverflows(t *testing.T) {
	start := t0
	end := start.Add(5 * time.Millisecond)
	for _, f := range []float64{1e-3, 1e-12, 1e-200, 0} {
		l := paceLimits{Share: 0.25, Rate: 200, MaxWait: time.Minute}.scaled(f)
		next := l.next(start, end)
		if next.Before(end) || next.Sub(end) > time.Minute {
			t.Errorf("factor %g: next call %s after the end, want within [0, 1m]", f, next.Sub(end))
		}
	}
	// The breaker's factor has a floor (outlook.breaker.min_factor).
	s := breakerSettings{resumeFactor: 0.01, minFactor: 0.1, window: 24 * time.Hour}
	b := breaker{Losses: make([]time.Time, 50)}
	for i := range b.Losses {
		b.Losses[i] = t0
	}
	if f := b.factor(t0, s); f != 0.1 || math.IsNaN(f) {
		t.Fatalf("factor after 50 losses %v, want the floor 0.1", f)
	}
}

// #3: on a first connect (and after the user's resume) an Outlook started
// moments ago was read at full pace; min_uptime only applied after a loss
// (failed first: factor 1). Now it is read at the reduced pace until it
// has been up min_uptime — never refused.
func TestAYoungOutlookIsReadAtTheReducedPace(t *testing.T) {
	o := profile()
	c, clk := testConnector(t, o, nil)
	o.startedAt = clk.Now().Add(-2 * time.Minute)
	if _, err := c.Discover(t.Context()); err != nil {
		t.Fatalf("a young Outlook is read, not refused: %v", err)
	}
	b := c.Breaker()
	if b.Factor != 0.5 || b.State != BreakerEasing || !b.Until.Equal(o.startedAt.Add(15*time.Minute)) {
		t.Fatalf("young Outlook: %+v, want easing at factor 0.5 until +15m", b)
	}
	clk.Advance(14 * time.Minute)
	if b := c.Breaker(); b.Factor != 1 || b.State != BreakerClosed {
		t.Fatalf("Outlook up 16 min: %+v", b)
	}
	// Resume keeps the uptime rule.
	for range 3 {
		clk.Advance(3 * time.Hour) // past the cooldown, Outlook up long enough
		if _, err := c.Discover(t.Context()); err != nil {
			t.Fatal(err)
		}
		o.crash()
		_, _ = c.Discover(t.Context())
		o.restart()
	}
	if c.Breaker().State != BreakerStopped {
		t.Fatalf("not stopped: %+v", c.Breaker())
	}
	if err := c.Resume(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if b := c.Breaker(); b.Factor != 0.5 || b.State != BreakerEasing {
		t.Fatalf("resumed onto an Outlook started moments ago: %+v", b)
	}
}

// #3: attaching failed with a lost-connection code (Outlook dying while it
// was being attached) read as "not running" and was never counted as a
// loss (failed first).
func TestAnAttachThatLosesTheConnectionIsALoss(t *testing.T) {
	o := profile()
	c, _ := testConnector(t, o, nil)
	o.attachErr = hrServerUnavailable
	_, err := c.Discover(t.Context())
	if !errors.Is(err, ErrSessionLost) || !errors.Is(err, source.ErrUnavailable) {
		t.Fatalf("attach with RPC_S_SERVER_UNAVAILABLE: %v", err)
	}
	if b := c.Breaker(); b.Losses != 1 || b.State != BreakerCooling {
		t.Fatalf("breaker %+v", b)
	}
}

// Lower finding: a call still busy after every retry was a plain error
// (an item error in the detail pass, so the pass went on hammering a busy
// Outlook). It is ErrOutlookBusy, a source.ErrUnavailable that ends the
// run, and no loss (failed first).
func TestOutlookStayingBusyEndsTheRunWithoutALoss(t *testing.T) {
	o := profile()
	c, _ := testConnector(t, o, map[string]any{"outlook.com.retry.attempts": 2})
	folders, err := c.Discover(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	sk := &memSink{}
	if _, err := c.Sync(t.Context(), folderByRole(t, folders, "primary", "inbox"), "", sk); err != nil {
		t.Fatal(err)
	}
	o.busy = 1 << 20
	_, err = c.ReadDetails(t.Context(), refOf(sk.msgs[0]))
	var ie *source.ItemError
	if !errors.Is(err, ErrOutlookBusy) || !errors.Is(err, source.ErrUnavailable) || errors.As(err, &ie) {
		t.Fatalf("busy through every retry: %v", err)
	}
	if hr, _ := HResult(err); hr != hrCallRejected {
		t.Fatalf("the HRESULT must survive: %v", err)
	}
	o.busy = 0
	if b := c.Breaker(); b.Losses != 0 {
		t.Fatalf("busy is no lost connection: %+v", b)
	}
	if _, err := c.Discover(t.Context()); err != nil {
		t.Fatalf("the session survives a busy Outlook: %v", err)
	}
}

// #4: RPC_E_SERVERFAULT raised by one item ended the run as a lost
// connection; the item stayed partial and, newest first, was read first
// on every run — three runs, and the source stopped with Outlook alive.
// The connector now names the item (a quarantine item error) and still
// counts the loss (failed first: no item named).
func TestAnItemThatFaultsOutlookIsNamedAndCounted(t *testing.T) {
	o := profile()
	c, _ := testConnector(t, o, nil)
	folders, err := c.Discover(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	sk := &memSink{}
	if _, err := c.Sync(t.Context(), folderByRole(t, folders, "primary", "inbox"), "", sk); err != nil {
		t.Fatal(err)
	}
	bad := refOf(sk.msgs[0])
	o.item(bad.StoreID, bad.ID).failBody = hrServerFault
	_, err = c.ReadDetails(t.Context(), bad)
	var ie *source.ItemError
	if !errors.As(err, &ie) || !ie.Quarantine || !errors.Is(err, ErrSessionLost) || !errors.Is(err, source.ErrUnavailable) {
		t.Fatalf("server fault: %v (%+v)", err, ie)
	}
	if b := c.Breaker(); b.Losses != 1 {
		t.Fatalf("the fault still counts as a loss: %+v", b)
	}
	// A plain item failure is no quarantine and no loss.
	o.restart()
	c2, _ := testConnector(t, o, nil)
	if _, err := c2.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	o.item(bad.StoreID, bad.ID).failBody = hrException
	if _, err := c2.ReadDetails(t.Context(), bad); !errors.As(err, &ie) || ie.Quarantine || errors.Is(err, source.ErrUnavailable) {
		t.Fatalf("plain item failure: %v", err)
	}
}

// Lower finding: c.mu was held for whole operations, so Breaker, Resume
// and Close waited behind a long sync (failed first: they blocked until
// the call returned). State reads and actions no longer wait; calls stay
// one at a time.
func TestStateAndActionsDoNotWaitForAnOperation(t *testing.T) {
	o := profile()
	c, _ := testConnector(t, o, nil)
	inCall, release := make(chan struct{}), make(chan struct{})
	once := false
	o.onCall = func(key string) {
		if key == "Stores.Count" && !once {
			once = true
			close(inCall)
			<-release
		}
	}
	done := make(chan error, 1)
	go func() { _, err := c.Discover(t.Context()); done <- err }()
	<-inCall
	actions := make(chan struct{})
	go func() {
		_ = c.Breaker()
		_ = c.Resume()
		_ = c.Stores()
		close(actions)
	}()
	select {
	case <-actions:
	case <-time.After(10 * time.Second): // failure path only: the actions waited for the call
		close(release)
		t.Fatal("Breaker/Resume/Stores waited for the operation in progress")
	}
	closed := make(chan struct{})
	go func() { c.Close(); close(closed) }()
	// release the call only once Close has marked the connector closed
	// (releasing first raced Close: the operation could finish before it,
	// a flaky failure seen under load)
	for !c.isClosed() {
		runtime.Gosched()
	}
	close(release)
	<-closed
	if err := <-done; err == nil {
		t.Fatal("an operation interrupted by Close must not succeed")
	}
	if _, err := c.Discover(t.Context()); !errors.Is(err, ErrClosed) {
		t.Fatalf("after Close: %v", err)
	}
}

// memKeeper is a source.StateKeeper in memory (the store's is tested in store).
type memKeeper map[string][]byte

func (k memKeeper) LoadState(_ context.Context, src, name string) ([]byte, error) {
	return k[src+"/"+name], nil
}
func (k memKeeper) SaveState(_ context.Context, src, name string, v []byte) error {
	k[src+"/"+name] = append([]byte(nil), v...)
	return nil
}

// Lower finding: the breaker's state lived in memory, so restarting the
// app forgot the losses and a stop (failed first: a new connector
// reconnected at once). With a keeper it survives the restart.
func TestTheBreakerSurvivesAnAppRestart(t *testing.T) {
	o := profile()
	keep := memKeeper{}
	c, clk := testConnector(t, o, nil)
	if err := c.Keep(t.Context(), keep); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	o.crash()
	_, _ = c.Discover(t.Context())
	o.restart()
	c.Close()
	// The app restarts at once: the new connector still cools down.
	c2, err := newConnector("outlook", c.cfg, nil, clk, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c2.Close)
	if err := c2.Keep(t.Context(), keep); err != nil {
		t.Fatal(err)
	}
	calls := len(o.calls)
	if _, err := c2.Discover(t.Context()); !errors.Is(err, ErrCoolingDown) || len(o.calls) != calls {
		t.Fatalf("after a restart: %v (%d calls)", err, len(o.calls)-calls)
	}
	if b := c2.Breaker(); b.Losses != 1 || b.State != BreakerCooling || b.Error == "" {
		t.Fatalf("restored state %+v", b)
	}
	// A stop survives too, and the user's resume is kept.
	for range 2 {
		clk.Advance(3 * time.Hour)
		if _, err := c2.Discover(t.Context()); err != nil {
			t.Fatal(err)
		}
		o.crash()
		_, _ = c2.Discover(t.Context())
		o.restart()
	}
	c3, _ := newConnector("outlook", c.cfg, nil, clk, o)
	t.Cleanup(c3.Close)
	if err := c3.Keep(t.Context(), keep); err != nil || c3.Breaker().State != BreakerStopped {
		t.Fatalf("stop not restored: %v %+v", err, c3.Breaker())
	}
	if err := c3.Resume(); err != nil {
		t.Fatal(err)
	}
	c4, _ := newConnector("outlook", c.cfg, nil, clk, o)
	t.Cleanup(c4.Close)
	if err := c4.Keep(t.Context(), keep); err != nil || c4.Breaker().Losses != 0 {
		t.Fatalf("resume not kept: %v %+v", err, c4.Breaker())
	}
	// Misuse: no keeper, a corrupt kept state.
	if err := c4.Keep(t.Context(), nil); err == nil {
		t.Fatal("Keep(nil) must fail")
	}
	keep["outlook/"+stateBreaker] = []byte("{not json")
	if err := c4.Keep(t.Context(), keep); err == nil {
		t.Fatal("a corrupt kept state must be reported")
	}
}

// #1: the live tests ran a whole-profile Discover (7 stores, 589 folders,
// ≈2,570 calls, 570 folders on online delegates) before anything was
// picked: every unpicked store is walked once for its counts. With
// outlook.discover.count_unpicked off it is listed with its kind and
// identity only (failed first: the setting did not exist).
func TestUnpickedStoresAreNotWalkedWhenCountingIsOff(t *testing.T) {
	o := profile()
	c, _ := testConnector(t, o, map[string]any{source.SettingSelected: []any{}, "outlook.discover.count_unpicked": false})
	if _, err := c.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := callsTo(o, "Folders.GetFirst") + callsTo(o, "Store.GetRootFolder"); n != 0 {
		t.Fatalf("%d folder walks with nothing picked and counting off", n)
	}
	boxes := c.Mailboxes()
	if len(boxes) != 2 {
		t.Fatalf("every store is still listed: %+v", boxes)
	}
	for _, b := range boxes {
		if b.Folders != -1 || b.Items != -1 || b.Key == "" || b.Kind == "" {
			t.Fatalf("an uncounted store: %+v", b)
		}
	}
}

// Counting unpicked stores is off by default: a first run must not walk
// every delegate mailbox (thousands of calls, and an offline store can
// block). The default is pinned here, not only in the schema.
func TestUnpickedStoresAreNotCountedByDefault(t *testing.T) {
	o := profile()
	c, _ := testConnector(t, o, map[string]any{source.SettingSelected: []any{}})
	if _, err := c.Discover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := callsTo(o, "Folders.GetFirst") + callsTo(o, "Store.GetRootFolder"); n != 0 {
		t.Fatalf("%d folder walks with the default (counting off)", n)
	}
	for _, b := range c.Mailboxes() {
		if b.Folders != -1 || b.Items != -1 {
			t.Fatalf("a store counted with the default: %+v", b)
		}
	}
}

// isClosed reports whether Close has begun (test support).
func (c *Connector) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}
