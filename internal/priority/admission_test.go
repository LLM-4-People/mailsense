package priority

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/route"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// errTestLimit is the fake guard's typed refusal. The service wraps it with
// ErrPaused but must keep the original reachable with errors.Is.
var errTestLimit = &testLimitError{resource: "cpu", percent: 91, limit: 85}

type testLimitError struct {
	resource string
	percent  int
	limit    int
}

func (e *testLimitError) Error() string {
	return "resource: " + e.resource + " above its limit"
}

// fakeAdmission is a paused admission with a count. allow admits the first
// allow calls, then returns err; a nil err always admits.
type fakeAdmission struct {
	mu    sync.Mutex
	err   error
	allow int
	calls int
}

func (f *fakeAdmission) Admit(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err == nil {
		return nil
	}
	if f.allow > 0 {
		f.allow--
		return nil
	}
	return f.err
}

func (f *fakeAdmission) set(err error, allow int) {
	f.mu.Lock()
	f.err, f.allow = err, allow
	f.mu.Unlock()
}

func (f *fakeAdmission) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// admittedService builds the fake-store service with the fake admission.
func admittedService(t *testing.T, rows []store.PriorityRow, adm Admission) (*Service, *fakeStore) {
	t.Helper()
	cfg := configtest.New(t, nil)
	fs := &fakeStore{rows: rows}
	svc, err := New(cfg, Deps{Store: fs, Gate: allowGate{}, Config: cfg, Clock: &fakeClock{now: testNow},
		Admission: adm, RouteKey: testRouteKey})
	if err != nil {
		t.Fatal(err)
	}
	return svc, fs
}

func cachedSnapshot(t *testing.T, svc *Service) *cachedRanking {
	t.Helper()
	svc.mu.Lock()
	defer svc.mu.Unlock()
	return svc.cached
}

// A cold ranking has nothing honest to serve above a ceiling: it must refuse
// before reading the mailbox, with the guard's error still discoverable.
func TestColdRankingIsRefusedWhileAdmissionIsPaused(t *testing.T) {
	adm := &fakeAdmission{err: errTestLimit}
	svc, fs := admittedService(t, []store.PriorityRow{coldRow(1)}, adm)

	for _, read := range []func() error{
		func() error {
			_, _, _, err := svc.Rank(t.Context(), FilterAll, 8)
			return err
		},
		func() error {
			_, err := svc.RankPage(t.Context(), FilterAll, 8, 0)
			return err
		},
	} {
		err := read()
		if !errors.Is(err, ErrPaused) {
			t.Fatalf("cold read error = %v, want ErrPaused", err)
		}
		if !errors.Is(err, errTestLimit) {
			t.Fatalf("cold read error = %v, want the guard's typed error in the chain", err)
		}
		var typed *testLimitError
		if !errors.As(err, &typed) || typed != errTestLimit {
			t.Fatalf("cold read error = %v, want errors.As to recover the guard's typed error", err)
		}
	}
	if got := fs.callCount(); got != 0 {
		t.Fatalf("a paused cold read scanned the mailbox %d times", got)
	}
	if got := cachedSnapshot(t, svc); got != nil {
		t.Fatalf("a paused cold read installed a snapshot: %+v", got)
	}
	if adm.count() == 0 {
		t.Fatal("the paused cold read never asked admission")
	}
}

// SetAdmission is the API wire's late binding: the service built without a
// guard accepts one later, and clearing it restores the unguarded behavior.
func TestSetAdmissionLateBinding(t *testing.T) {
	svc, fs := admittedService(t, []store.PriorityRow{coldRow(1)}, nil)
	svc.SetAdmission(&fakeAdmission{err: errTestLimit})
	if _, _, _, err := svc.Rank(t.Context(), FilterAll, 8); !errors.Is(err, ErrPaused) {
		t.Fatalf("late-bound paused read = %v, want ErrPaused", err)
	}
	if got := fs.callCount(); got != 0 {
		t.Fatalf("the late-bound pause scanned the mailbox %d times", got)
	}
	svc.SetAdmission(nil)
	if items, _, _, err := svc.Rank(t.Context(), FilterAll, 8); err != nil || len(items) != 1 {
		t.Fatalf("cleared admission read = %v %v", items, err)
	}
}

// A good cached ranking outranks the guard's ceiling: the stale snapshot is
// served with no error and no background scan while paused.
func TestPausedRefreshServesTheCachedRanking(t *testing.T) {
	adm := &fakeAdmission{}
	svc, fs := admittedService(t, []store.PriorityRow{coldRow(1)}, adm)
	first, err := svc.ranking(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	adm.set(errTestLimit, 0)
	fs.mu.Lock()
	fs.mailGen++
	fs.mu.Unlock()

	second, err := svc.ranking(t.Context())
	if err != nil {
		t.Fatalf("a paused refresh reached the caller: %v", err)
	}
	if second != first {
		t.Fatal("a paused refresh replaced the cached ranking")
	}
	if got := fs.callCount(); got != 1 {
		t.Fatalf("a paused refresh scanned the mailbox (%d scans)", got)
	}
	time.Sleep(50 * time.Millisecond) // a wrongly started refresh would scan by now
	if got := fs.callCount(); got != 1 {
		t.Fatalf("a paused refresh started in the background (%d scans)", got)
	}
	svc.mu.Lock()
	refreshing := svc.refreshing
	svc.mu.Unlock()
	if refreshing {
		t.Fatal("a paused refresh was left marked in flight")
	}
}

// When the host recovers the next read refreshes and installs again.
func TestAdmissionClearsAndTheNextReadRebuilds(t *testing.T) {
	adm := &fakeAdmission{}
	svc, fs := admittedService(t, []store.PriorityRow{coldRow(1)}, adm)
	if _, err := svc.ranking(t.Context()); err != nil {
		t.Fatal(err)
	}

	adm.set(errTestLimit, 0)
	fs.mu.Lock()
	fs.mailGen++
	fs.mu.Unlock()
	if _, err := svc.ranking(t.Context()); err != nil {
		t.Fatal(err)
	}

	fs.mu.Lock()
	fs.rows = append(fs.rows, coldRow(2))
	fs.mailGen++
	fs.mu.Unlock()
	adm.set(nil, 0)

	if items, total, _, err := svc.Rank(t.Context(), FilterAll, 8); err != nil || len(items) != 1 || total != 1 {
		t.Fatalf("the recovery read = %d items total=%d err=%v, want the stale snapshot first", len(items), total, err)
	}
	waitPriorityIdle(t, svc)
	items, total, _, err := svc.Rank(t.Context(), FilterAll, 8)
	if err != nil || len(items) != 2 || total != 2 {
		t.Fatalf("the rebuilt read = %d items total=%d err=%v", len(items), total, err)
	}
}

// A cold build paused after the scan abandons: the scan happened, but no
// snapshot is installed and the caller sees the guard's error.
func TestColdBuildPausedAfterTheScanInstallsNothing(t *testing.T) {
	// Admission calls: the request's pre-build check, the build's pre-scan
	// check, then the post-scan check refuses.
	adm := &fakeAdmission{err: errTestLimit, allow: 2}
	svc, fs := admittedService(t, []store.PriorityRow{coldRow(1)}, adm)

	_, _, _, err := svc.Rank(t.Context(), FilterAll, 8)
	if !errors.Is(err, ErrPaused) {
		t.Fatalf("mid-build pause error = %v, want ErrPaused", err)
	}
	if got := fs.callCount(); got != 1 {
		t.Fatalf("the abandoned cold build scanned %d times, want the one scan before the pause", got)
	}
	if got := cachedSnapshot(t, svc); got != nil {
		t.Fatalf("the abandoned cold build installed a snapshot: %+v", got)
	}

	// The next admitted request retries from a clean cold state.
	adm.set(nil, 0)
	if items, total, _, err := svc.Rank(t.Context(), FilterAll, 8); err != nil || len(items) != 1 || total != 1 {
		t.Fatalf("the retry after the pause = %d items total=%d err=%v", len(items), total, err)
	}
	if got := fs.callCount(); got != 2 {
		t.Fatalf("scans after the retry = %d, want the abandoned scan plus the successful one", got)
	}
}

// A refresh paused after the scan keeps the previous snapshot byte for byte:
// the new candidate is dropped, not installed.
func TestRefreshPausedAfterTheScanKeepsThePreviousSnapshot(t *testing.T) {
	adm := &fakeAdmission{}
	svc, fs := admittedService(t, []store.PriorityRow{coldRow(1)}, adm)
	before, err := svc.ranking(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	fs.mu.Lock()
	fs.rows = append(fs.rows, coldRow(2))
	fs.mailGen++
	fs.mu.Unlock()
	// Admission calls: the cached-branch pre-refresh check, the build's
	// pre-scan check, then the post-scan check refuses.
	adm.set(errTestLimit, 2)

	items, total, _, err := svc.Rank(t.Context(), FilterAll, 8)
	if err != nil || len(items) != 1 || total != 1 {
		t.Fatalf("the paused refresh's read = %d items total=%d err=%v, want the cached snapshot", len(items), total, err)
	}
	waitPriorityIdle(t, svc)
	after := cachedSnapshot(t, svc)
	if after != before {
		t.Fatal("the abandoned refresh replaced the cached snapshot")
	}
	if len(after.items) != 1 || after.counts[FilterAll] != 1 {
		t.Fatalf("the cached snapshot changed: %d items, all=%d", len(after.items), after.counts[FilterAll])
	}
	if got := fs.callCount(); got != 2 {
		t.Fatalf("scans = %d, want the first build plus the abandoned refresh", got)
	}
}

// A preview flip while a refresh is in flight must not start a second full
// scan beside it. The cold snapshot waits for the one refresh, then builds.
func TestPreviewFlipDoesNotScanBesideARefresh(t *testing.T) {
	row := store.PriorityRow{MessageID: 1, ReceivedAt: receivedAgo(0),
		Route:   map[string]route.Answer{"reply_needed": notCertified(engine.Noul)},
		Preview: map[string]engine.Answer{"reply_needed": {Type: engine.Noul, Value: 0.9}}}
	fs := &blockingStore{rows: []store.PriorityRow{row}}
	cfg := configtest.New(t, nil)
	var preview atomic.Bool
	svc, err := New(cfg, Deps{Store: fs, Gate: settingGate{preview: preview.Load}, Config: cfg,
		Clock: &fakeClock{now: testNow}, RouteKey: testRouteKey})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, got, err := svc.Rank(t.Context(), FilterReply, 8); err != nil || got || len(everyItem(t, svc)) != 0 {
		t.Fatalf("route-only warm: err=%v preview=%v", err, got)
	}

	fs.mu.Lock()
	fs.mailGen++
	fs.mu.Unlock()
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	fs.block(started, release)
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()

	// This read serves the cache and starts the one background refresh.
	if _, _, _, err := svc.Rank(t.Context(), FilterReply, 8); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the refresh never reached the store")
	}
	if got := fs.callCount(); got != 2 {
		close(release)
		released = true
		t.Fatalf("scans before the flip = %d, want the warm and the refresh", got)
	}

	// Flip the preview. The flip needs a synchronous snapshot for the new
	// flag; it must wait for the refresh rather than scan beside it.
	preview.Store(true)
	type result struct {
		items   []Item
		preview bool
		err     error
	}
	done := make(chan result, 1)
	go func() {
		items, _, got, err := svc.Rank(context.Background(), FilterReply, 8)
		done <- result{items: items, preview: got, err: err}
	}()
	select {
	case got := <-done:
		close(release)
		released = true
		t.Fatalf("the preview flip returned while the refresh was still scanning: %+v", got)
	case <-time.After(150 * time.Millisecond):
	}
	if got := fs.callCount(); got != 2 {
		close(release)
		released = true
		t.Fatalf("the preview flip started a second scan beside the refresh (%d scans)", got)
	}

	close(release)
	released = true
	var got result
	select {
	case got = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the preview flip did not return")
	}
	if got.err != nil || !got.preview || len(got.items) != 1 {
		t.Fatalf("the preview flip = %d items preview=%v err=%v", len(got.items), got.preview, got.err)
	}
	if scans := fs.callCount(); scans != 3 {
		t.Fatalf("scans after the flip = %d, want the warm, the refresh and the cold build", scans)
	}
}

func everyItem(t *testing.T, svc *Service) []Item {
	t.Helper()
	c, err := svc.ranking(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	items := make([]Item, 0, len(c.items))
	for _, r := range c.items {
		items = append(items, r.item)
	}
	return items
}

// TestAdmissionCacheRace drives reads, rebuilds and admission flips together
// so `go test -race` covers the shared cache and the late-bound guard.
func TestAdmissionCacheRace(t *testing.T) {
	adm := &fakeAdmission{}
	svc, fs := admittedService(t, []store.PriorityRow{coldRow(1)}, adm)
	if _, err := svc.ranking(t.Context()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 400*time.Millisecond)
	defer cancel()
	var wg sync.WaitGroup
	var paused atomic.Int64
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if i%2 == 0 {
				adm.set(errTestLimit, 0)
			} else {
				adm.set(nil, 0)
				paused.Add(1)
			}
			fs.mu.Lock()
			fs.mailGen++
			fs.mu.Unlock()
			time.Sleep(time.Millisecond)
		}
	}()

	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				page, err := svc.RankPage(ctx, FilterAll, 8, 0)
				if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, ErrPaused) {
					t.Errorf("read during admission flips: %v", err)
					return
				}
				if err == nil && page.Total == 0 {
					t.Errorf("a served page lost its cached candidates: %+v", page)
					return
				}
				if _, err := svc.ThreadStates(ctx); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, ErrPaused) {
					t.Errorf("thread states during admission flips: %v", err)
					return
				}
			}
		}()
	}
	<-ctx.Done()
	close(stop)
	wg.Wait()
	if paused.Load() == 0 {
		t.Fatal("the race test never admitted a read")
	}
}

// admissionFunc adapts a function to Admission so a test can inspect the
// context the guard receives.
type admissionFunc func(context.Context) error

func (f admissionFunc) Admit(ctx context.Context) error { return f(ctx) }

// TestAdmissionRunsInsideTheBoundedBuildContext pins where the guard's last
// word runs after the build moved to safe.Detached: on the build's own bounded,
// caller-detached context, after the scan, never on the request's context. A
// rewrite that hands the guard the caller's context, or asks it before the
// scan, fails here instead of installing a snapshot the guard should have
// refused.
func TestAdmissionRunsInsideTheBoundedBuildContext(t *testing.T) {
	caller, cancel := context.WithCancel(t.Context())
	defer cancel()
	type call struct{ deadline, detached, scanned bool }
	var mu sync.Mutex
	var calls []call
	var fs *fakeStore
	adm := admissionFunc(func(ctx context.Context) error {
		_, deadline := ctx.Deadline()
		mu.Lock()
		calls = append(calls, call{deadline: deadline, detached: ctx != caller, scanned: fs != nil && fs.callCount() > 0})
		mu.Unlock()
		return nil
	})
	svc, fs := admittedService(t, []store.PriorityRow{coldRow(1)}, adm)
	if _, _, _, err := svc.Rank(caller, FilterAll, 8); err != nil {
		t.Fatalf("cold ranking read: %v", err)
	}

	mu.Lock()
	got := append([]call(nil), calls...)
	mu.Unlock()
	if len(got) < 2 {
		t.Fatalf("admission asked %d times, want the request check and the build's", len(got))
	}
	if got[0].deadline || got[0].detached {
		t.Fatalf("the request's own admission check got the build context: %+v", got[0])
	}
	for i, c := range got[1:] {
		if !c.deadline || !c.detached {
			t.Fatalf("build admission call %d ran outside the bounded detached context: %+v", i+1, c)
		}
	}
	if last := got[len(got)-1]; !last.scanned {
		t.Fatal("the guard's last word ran before the scan")
	}
}
