package priority

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/route"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
)

func TestFreshnessOutlastsASlowBuild(t *testing.T) {
	if got := freshness(&cachedRanking{build: time.Second}); got != CacheTTL {
		t.Fatalf("a fast build freshness = %s, want %s", got, CacheTTL)
	}
	if got, want := freshness(&cachedRanking{build: 40 * time.Second}), 120*time.Second; got != want {
		t.Fatalf("a 40s build freshness = %s, want %s", got, want)
	}
	if got := freshness(&cachedRanking{build: 0}); got != CacheTTL {
		t.Fatalf("an unmeasured build freshness = %s, want %s", got, CacheTTL)
	}
	if got, want := freshness(&cachedRanking{build: time.Hour}), maxCacheTTL; got != want {
		t.Fatalf("a pathological build freshness = %s, want the %s cap", got, want)
	}
}

// steppingClock advances on every Now call, so a test can make one build
// take longer than CacheTTL without sleeping.
type steppingClock struct {
	mu   sync.Mutex
	now  time.Time
	step time.Duration
}

func TestFreshnessIsAnchoredAtInstallationNotBuildStart(t *testing.T) {
	row := store.PriorityRow{MessageID: 1, Subject: "s", ReceivedAt: receivedAgo(0),
		Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)}}
	cfg := configtest.New(t, nil)
	fs := &fakeStore{rows: []store.PriorityRow{row}}
	clk := &steppingClock{now: testNow, step: 20 * time.Second}
	svc, err := New(cfg, Deps{Store: fs, Gate: allowGate{}, Config: cfg, Clock: clk, RouteKey: testRouteKey})
	if err != nil {
		t.Fatal(err)
	}
	c, err := svc.buildRanking(t.Context(), false, clk.Now(), "priority-test-route")
	if err != nil {
		t.Fatal(err)
	}
	if want := testNow.Add(40 * time.Second); !c.at.Equal(want) {
		t.Fatalf("snapshot anchored at %s, want the end of the build %s", c.at, want)
	}
	if c.build != 20*time.Second {
		t.Fatalf("measured build %s, want 20s", c.build)
	}
	if got, want := freshness(c), 60*time.Second; got != want {
		t.Fatalf("freshness %s, want %s", got, want)
	}
}
func TestServiceCacheAndBounds(t *testing.T) {
	row := store.PriorityRow{MessageID: 1, Subject: "s", ReceivedAt: receivedAgo(0),
		Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)}}
	cfg := configtest.New(t, nil)
	fs := &fakeStore{rows: []store.PriorityRow{row}}
	clk := &fakeClock{now: testNow}
	svc, err := New(cfg, Deps{Store: fs, Gate: allowGate{}, Config: cfg, Clock: clk, RouteKey: testRouteKey})
	if err != nil {
		t.Fatal(err)
	}
	items, total, preview, err := svc.Rank(t.Context(), FilterReply, 5)
	if err != nil || len(items) != 1 || total != 1 || preview {
		t.Fatalf("rank: %v %d %v %v", items, total, preview, err)
	}
	if fs.callCount() != 1 {
		t.Fatalf("store calls %d", fs.callCount())
	}
	if _, _, _, err := svc.Rank(t.Context(), FilterAll, 5); err != nil || fs.callCount() != 1 {
		t.Fatalf("cache miss: calls %d err %v", fs.callCount(), err)
	}
	clk.Advance(maxCacheTTL + time.Second)
	items, total, _, err = svc.Rank(t.Context(), FilterAll, 5)
	if err != nil || len(items) != 1 || total != 1 {
		t.Fatalf("expired cache did not serve stale: %v %d %v", items, total, err)
	}
	waitPriorityIdle(t, svc)
	if fs.callCount() != 2 {
		t.Fatalf("cache refresh: calls %d", fs.callCount())
	}
	for _, limit := range []int{0, MaxLimit + 1} {
		if _, _, _, err := svc.Rank(t.Context(), FilterAll, limit); err == nil {
			t.Fatalf("limit %d accepted", limit)
		}
	}
	if _, _, _, err := svc.Rank(t.Context(), Filter("bogus"), 1); err == nil {
		t.Fatal("unknown filter accepted")
	}
	if _, err := svc.Top(t.Context(), 0); err == nil {
		t.Fatal("top 0 accepted")
	}
}

// An idle mailbox keeps one snapshot for the whole maxCacheTTL window: its
// mail rows, route answers, route key and preview are unchanged, so only
// age-dependent scores can drift. A one-second idle clock for a minute used
// to rebuild at every CacheTTL (four refreshes); it now keeps the one cold
// build until the maxCacheTTL bound expires.
func TestRankingKeepsAnUnchangedSnapshotPastCacheTTL(t *testing.T) {
	row := store.PriorityRow{MessageID: 1, Subject: "s", ReceivedAt: receivedAgo(0),
		Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)}}
	cfg := configtest.New(t, nil)
	fs := &fakeStore{rows: []store.PriorityRow{row}}
	clk := &fakeClock{now: testNow}
	svc, err := New(cfg, Deps{Store: fs, Gate: allowGate{}, Config: cfg, Clock: clk, RouteKey: testRouteKey})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := svc.Rank(t.Context(), FilterAll, 5); err != nil {
		t.Fatal(err)
	}
	svc.mu.Lock()
	first := svc.cached
	svc.mu.Unlock()
	if first == nil {
		t.Fatal("the cold build installed no snapshot")
	}

	// A 60 s idle clock crosses four CacheTTL windows; none may rebuild.
	for range 60 {
		clk.Advance(time.Second)
		if _, _, _, err := svc.Rank(t.Context(), FilterAll, 5); err != nil {
			t.Fatal(err)
		}
	}
	if got := fs.callCount(); got != 1 {
		t.Fatalf("a 60 s idle clock ran %d rebuilds, want none", got-1)
	}
	svc.mu.Lock()
	unchanged := svc.cached == first
	svc.mu.Unlock()
	if !unchanged {
		t.Fatal("an unchanged snapshot was replaced inside maxCacheTTL")
	}

	// Past CacheTTL but inside maxCacheTTL it is still the same snapshot.
	clk.Advance(CacheTTL + 5*time.Second)
	if _, _, _, err := svc.Rank(t.Context(), FilterAll, 5); err != nil {
		t.Fatal(err)
	}
	if got := fs.callCount(); got != 1 {
		t.Fatalf("a read at %s rebuilt inside maxCacheTTL: %d store scans", clk.now.Sub(first.at), got)
	}

	// Crossing maxCacheTTL starts exactly one refresh and still serves the
	// old snapshot to the request that crosses it.
	clk.Advance(maxCacheTTL)
	if _, _, _, err := svc.Rank(t.Context(), FilterAll, 5); err != nil {
		t.Fatal(err)
	}
	refreshed := waitPriorityIdle(t, svc)
	if got := fs.callCount(); got != 2 {
		t.Fatalf("crossing maxCacheTTL ran %d builds, want exactly one refresh", got)
	}
	if refreshed == first {
		t.Fatal("crossing maxCacheTTL did not refresh the snapshot")
	}
	// The refreshed snapshot is fresh again: the next read keeps it.
	if _, _, _, err := svc.Rank(t.Context(), FilterAll, 5); err != nil {
		t.Fatal(err)
	}
	if got := fs.callCount(); got != 2 {
		t.Fatalf("the refreshed snapshot rebuilt again immediately: %d store scans", got)
	}
}

// A generation bump is a real input change: no TTL wait may hide it. Mail and
// route generations each refresh on the next read, even one second after the
// snapshot was installed.
func TestRankingRebuildsImmediatelyOnAGenerationChange(t *testing.T) {
	old := store.PriorityRow{MessageID: 1, Subject: "old", ReceivedAt: receivedAgo(0),
		Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)}}
	fresh := store.PriorityRow{MessageID: 2, Subject: "new", ReceivedAt: receivedAgo(0),
		Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)}}
	cfg := configtest.New(t, nil)
	fs := &fakeStore{rows: []store.PriorityRow{old}}
	clk := &fakeClock{now: testNow}
	svc, err := New(cfg, Deps{Store: fs, Gate: allowGate{}, Config: cfg, Clock: clk, RouteKey: testRouteKey})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := svc.Rank(t.Context(), FilterAll, 5); err != nil {
		t.Fatal(err)
	}

	// New mail arrives one second after the snapshot: the request serves the
	// old snapshot and rebuilds behind it instead of waiting for CacheTTL.
	clk.Advance(time.Second)
	fs.rows = []store.PriorityRow{old, fresh}
	fs.mailGen = 1
	if items, _, _, err := svc.Rank(t.Context(), FilterAll, 5); err != nil || len(items) != 1 {
		t.Fatalf("a mail generation change must serve the old snapshot behind the refresh: %v %v", items, err)
	}
	c := waitPriorityIdle(t, svc)
	if got := fs.callCount(); got != 2 {
		t.Fatalf("a mail generation change ran %d store scans, want one refresh", got)
	}
	if c == nil || c.mailGen != 1 || len(c.items) != 2 {
		t.Fatalf("the mail generation refresh did not install the new rows: %+v", c)
	}

	// A commit writes route answers under the same mail one second after the
	// refreshed snapshot: it must refresh immediately too.
	clk.Advance(time.Second)
	fs.routeGen = 1
	if items, _, _, err := svc.Rank(t.Context(), FilterAll, 5); err != nil || len(items) != 2 {
		t.Fatalf("a route generation change must serve the current snapshot: %v %v", items, err)
	}
	c = waitPriorityIdle(t, svc)
	if got := fs.callCount(); got != 3 {
		t.Fatalf("a route generation change ran %d store scans, want one refresh", got)
	}
	if c == nil || c.routeGen != 1 || len(c.items) != 2 {
		t.Fatalf("the route generation refresh did not install the new snapshot: %+v", c)
	}
}
func TestServicePreviewCacheFlagAndErrors(t *testing.T) {
	row := store.PriorityRow{MessageID: 1, ReceivedAt: receivedAgo(0),
		Route:   map[string]route.Answer{"reply_needed": notCertified(engine.Noul)},
		Preview: map[string]engine.Answer{"reply_needed": {Type: engine.Noul, Value: 0.9}}}
	svc, fs := newService(t, []store.PriorityRow{row}, true)
	items, _, preview, err := svc.Rank(t.Context(), FilterReply, 1)
	if err != nil || !preview || len(items) != 1 || items[0].Signals.ReplyNeeded != 1 || !items[0].Preview {
		t.Fatalf("preview ranking: %+v %v %v", items, preview, err)
	}
	if flags := fs.previewFlags(); len(flags) != 1 || !flags[0] {
		t.Fatalf("preview flag not passed to the store: %v", flags)
	}
	fs.setErr(errors.New("boom"))
	clk := &fakeClock{now: testNow}
	// A new service (empty cache) surfaces the store error.
	cfg := configtest.New(t, nil)
	bad, _ := New(cfg, Deps{Store: fs, Gate: allowGate{}, Config: cfg, Clock: clk, RouteKey: testRouteKey})
	if _, _, _, err := bad.Rank(t.Context(), FilterAll, 1); err == nil {
		t.Fatal("a store error must fail the read")
	}
}

type fakeClock struct{ now time.Time }

func TestServicePreviewFlagSeparatesTheCache(t *testing.T) {
	row := store.PriorityRow{MessageID: 1, ReceivedAt: receivedAgo(0),
		Route:   map[string]route.Answer{"reply_needed": notCertified(engine.Noul), "urgency": score("urgency", 4)},
		Preview: map[string]engine.Answer{"reply_needed": {Type: engine.Noul, Value: 0.9}}}
	cfg := configtest.New(t, nil)
	fs := &fakeStore{rows: []store.PriorityRow{row}}
	clk := &fakeClock{now: testNow}
	svc, err := New(cfg, Deps{Store: fs, Gate: settingGate{preview: cfg.Preview}, Config: cfg, Clock: clk, RouteKey: testRouteKey})
	if err != nil {
		t.Fatal(err)
	}
	items, _, preview, err := svc.Rank(t.Context(), FilterAll, 1)
	if err != nil || preview || len(items) != 1 || items[0].Preview || items[0].Signals.ReplyNeeded != 0 {
		t.Fatalf("preview off: %+v %v %v", items, preview, err)
	}
	if err := cfg.Set("learning.test_mode", true); err != nil {
		t.Fatal(err)
	}
	items, _, preview, err = svc.Rank(t.Context(), FilterReply, 1)
	if err != nil || !preview || len(items) != 1 || !items[0].Preview || items[0].Signals.ReplyNeeded != 1 {
		t.Fatalf("preview on: %+v %v %v", items, preview, err)
	}
	if flags := fs.previewFlags(); len(flags) != 2 || flags[0] || !flags[1] {
		t.Fatalf("store preview flags %v", flags)
	}
}

// waitUntil polls cond until it holds, failing the test instead of hanging.
func TestRankingServesStaleAcrossAGenerationChange(t *testing.T) {
	old := store.PriorityRow{MessageID: 1, Subject: "old", ReceivedAt: receivedAgo(0),
		Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)}}
	fresh := store.PriorityRow{MessageID: 2, Subject: "new", ReceivedAt: receivedAgo(0),
		Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)}}
	clk := &fakeClock{now: testNow}
	svc, fs := newBlockingService(t, []store.PriorityRow{old}, clk)
	if _, _, _, err := svc.Rank(t.Context(), FilterAll, 8); err != nil {
		t.Fatal(err)
	}
	fs.mu.Lock()
	fs.mailGen++
	fs.mu.Unlock()

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	fs.block(started, release)

	served := make(chan []Item, 1)
	go func() {
		items, _, _, _ := svc.Rank(context.Background(), FilterAll, 8)
		served <- items
	}()
	select {
	case items := <-served:
		if len(items) != 1 || items[0].MessageID != 1 {
			close(release)
			t.Fatalf("stale serve = %v", items)
		}
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("a generation change blocked instead of serving the cached ranking")
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("the generation refresh never reached the store")
	}

	// While the rebuild runs, another request is served from the same
	// snapshot and does not launch a second build.
	if items, _, _, err := svc.Rank(t.Context(), FilterAll, 8); err != nil ||
		len(items) != 1 || items[0].MessageID != 1 {
		close(release)
		t.Fatalf("concurrent stale serve: %v %v", items, err)
	}
	if got := fs.callCount(); got != 2 {
		close(release)
		t.Fatalf("the generation refresh was not single-flight: %d store calls", got)
	}

	fs.setRows([]store.PriorityRow{old, fresh})
	close(release)
	waitUntil(t, "the ranking rebuilt for the new generation", func() bool {
		svc.mu.Lock()
		defer svc.mu.Unlock()
		return svc.cached != nil && len(svc.cached.items) == 2
	})
}
func TestRankingServesStaleWhileRefreshRuns(t *testing.T) {
	old := store.PriorityRow{MessageID: 1, Subject: "old", ReceivedAt: receivedAgo(0),
		Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)}}
	fresh := store.PriorityRow{MessageID: 2, Subject: "new", ReceivedAt: receivedAgo(0),
		Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)}}
	clk := &fakeClock{now: testNow}
	svc, fs := newBlockingService(t, []store.PriorityRow{old}, clk)
	if items, total, _, err := svc.Rank(t.Context(), FilterAll, 8); err != nil || len(items) != 1 ||
		items[0].MessageID != 1 || total != 1 {
		t.Fatalf("warm ranking: %v %d %v", items, total, err)
	}
	clk.Advance(maxCacheTTL + time.Second)

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	fs.block(started, release)

	type ranked struct {
		items []Item
		total int
		err   error
	}
	served := make(chan ranked, 1)
	go func() {
		items, total, _, err := svc.Rank(context.Background(), FilterAll, 8)
		served <- ranked{items: items, total: total, err: err}
	}()
	select {
	case got := <-served:
		if got.err != nil || len(got.items) != 1 || got.items[0].MessageID != 1 || got.total != 1 {
			close(release)
			t.Fatalf("stale serve = %+v", got)
		}
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("an expired ranking blocked instead of serving the old cache")
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("the background refresh never reached the store")
	}

	// While the refresh is blocked, another request is served from the old
	// snapshot and does not launch a second build.
	items, total, _, err := svc.Rank(t.Context(), FilterAll, 8)
	if err != nil || len(items) != 1 || items[0].MessageID != 1 || total != 1 {
		close(release)
		t.Fatalf("concurrent stale serve: %v %d %v", items, total, err)
	}
	if got := fs.callCount(); got != 2 {
		close(release)
		t.Fatalf("the background refresh was not single-flight: %d store calls", got)
	}

	fs.setRows([]store.PriorityRow{old, fresh})
	close(release)
	waitUntil(t, "the refreshed ranking", func() bool {
		svc.mu.Lock()
		defer svc.mu.Unlock()
		return svc.cached != nil && len(svc.cached.items) == 2
	})
	items, total, _, err = svc.Rank(t.Context(), FilterAll, 8)
	if err != nil || len(items) != 2 || total != 2 {
		t.Fatalf("refreshed ranking: %v %d %v", items, total, err)
	}
}
func TestRankingFailedRefreshKeepsCacheAndRetries(t *testing.T) {
	old := store.PriorityRow{MessageID: 1, Subject: "old", ReceivedAt: receivedAgo(0),
		Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)}}
	fresh := store.PriorityRow{MessageID: 2, Subject: "new", ReceivedAt: receivedAgo(0),
		Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)}}
	clk := &fakeClock{now: testNow}
	svc, fs := newBlockingService(t, []store.PriorityRow{old}, clk)
	if items, _, _, err := svc.Rank(t.Context(), FilterAll, 8); err != nil || len(items) != 1 {
		t.Fatalf("warm ranking: %v %v", items, err)
	}
	svc.mu.Lock()
	base := svc.cached
	svc.mu.Unlock()

	clk.Advance(maxCacheTTL + time.Second)
	fs.setErr(errors.New("store offline"))
	if items, _, _, err := svc.Rank(t.Context(), FilterAll, 8); err != nil || len(items) != 1 {
		t.Fatalf("a failing refresh must serve the old cache: %v %v", items, err)
	}
	cached := waitPriorityIdle(t, svc)
	if got := fs.callCount(); got < 2 {
		t.Fatalf("the refresh never read the store: %d calls", got)
	}
	svc.mu.Lock()
	refreshing := svc.refreshing
	svc.mu.Unlock()
	if refreshing {
		t.Fatal("a failed refresh left refreshing set")
	}
	if cached != base {
		t.Fatal("a failed refresh replaced the cached ranking")
	}

	// The cache is still expired, so the next request retries instead of
	// serving it as fresh forever.
	fs.setErr(nil)
	fs.setRows([]store.PriorityRow{old, fresh})
	if items, _, _, err := svc.Rank(t.Context(), FilterAll, 8); err != nil || len(items) != 1 {
		t.Fatalf("retry stale serve: %v %v", items, err)
	}
	waitUntil(t, "the retried ranking", func() bool {
		svc.mu.Lock()
		defer svc.mu.Unlock()
		return svc.cached != nil && len(svc.cached.items) == 2
	})
	if got := fs.callCount(); got < 3 {
		t.Fatalf("the retry never read the store: %d calls", got)
	}
	if items, total, _, err := svc.Rank(t.Context(), FilterAll, 8); err != nil || len(items) != 2 || total != 2 {
		t.Fatalf("retried ranking: %v %d %v", items, total, err)
	}
}
func TestRankingRefreshHeadErrorKeepsTheOldCache(t *testing.T) {
	old := store.PriorityRow{MessageID: 1, Subject: "old", ReceivedAt: receivedAgo(0), Body: "old body",
		Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)}}
	fresh := store.PriorityRow{MessageID: 2, Subject: "new", ReceivedAt: receivedAgo(0), Body: "new body",
		Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)}}
	clk := &fakeClock{now: testNow}
	svc, fs := newBlockingService(t, []store.PriorityRow{old}, clk)
	if items, _, _, err := svc.Rank(t.Context(), FilterAll, 8); err != nil || len(items) != 1 ||
		items[0].MessageID != 1 || items[0].Snippet == "" {
		t.Fatalf("warm ranking: %v %v", items, err)
	}
	svc.mu.Lock()
	base := svc.cached
	svc.mu.Unlock()

	clk.Advance(maxCacheTTL + time.Second)
	fs.setHeadErr(errors.New("heads offline"))
	if items, _, _, err := svc.Rank(t.Context(), FilterAll, 8); err != nil || len(items) != 1 ||
		items[0].MessageID != 1 || items[0].Snippet == "" {
		t.Fatalf("a failing refresh must serve the old cache: %v %v", items, err)
	}
	cached := waitPriorityIdle(t, svc)
	if cached != base {
		t.Fatal("a failed head batch replaced the cached ranking")
	}
	if len(cached.items) != 1 || cached.items[0].item.Snippet == "" {
		t.Fatalf("the cached snippet was lost: %+v", cached.items)
	}

	fs.setHeadErr(nil)
	fs.setRows([]store.PriorityRow{old, fresh})
	if items, _, _, err := svc.Rank(t.Context(), FilterAll, 8); err != nil || len(items) != 1 {
		t.Fatalf("retry stale serve: %v %v", items, err)
	}
	waitUntil(t, "the retried ranking", func() bool {
		svc.mu.Lock()
		defer svc.mu.Unlock()
		return svc.cached != nil && len(svc.cached.items) == 2
	})
	if items, total, _, err := svc.Rank(t.Context(), FilterAll, 8); err != nil || len(items) != 2 || total != 2 {
		t.Fatalf("retried ranking: %v %d %v", items, total, err)
	}
}
func TestRankingPreviewChangeBuildsSynchronously(t *testing.T) {
	row := store.PriorityRow{MessageID: 1, ReceivedAt: receivedAgo(0),
		Route:   map[string]route.Answer{"reply_needed": notCertified(engine.Noul)},
		Preview: map[string]engine.Answer{"reply_needed": {Type: engine.Noul, Value: 0.9}}}
	fs := &fakeStore{rows: []store.PriorityRow{row}}
	cfg := configtest.New(t, nil)
	clk := &fakeClock{now: testNow}
	svc, err := New(cfg, Deps{Store: fs, Gate: settingGate{preview: cfg.Preview}, Config: cfg, Clock: clk, RouteKey: testRouteKey})
	if err != nil {
		t.Fatal(err)
	}
	if items, _, preview, err := svc.Rank(t.Context(), FilterReply, 1); err != nil || preview ||
		len(items) != 0 {
		t.Fatalf("route-only ranking: %v %v %v", items, preview, err)
	}

	// A preview switch builds synchronously at any age: the cached snapshot
	// must not serve the route-only signals for another window.
	if err := cfg.Set("learning.test_mode", true); err != nil {
		t.Fatal(err)
	}
	items, _, preview, err := svc.Rank(t.Context(), FilterReply, 1)
	if err != nil || !preview || len(items) != 1 || items[0].Signals.ReplyNeeded != 1 {
		t.Fatalf("preview switch: %v %v %v", items, preview, err)
	}
	if flags := fs.previewFlags(); len(flags) != 2 || flags[0] || !flags[1] {
		t.Fatalf("preview switch did not build for the new preview: %v", flags)
	}
	svc.mu.Lock()
	refreshing := svc.refreshing
	svc.mu.Unlock()
	if refreshing {
		t.Fatal("a preview switch must build synchronously, not in the background")
	}
}

// A commit that writes new route answers under the same configuration must
// not serve the previous ranking for the TTL.
func TestRankingCacheFollowsRouteGeneration(t *testing.T) {
	rows := []store.PriorityRow{
		{MessageID: 1, ReceivedAt: receivedAgo(time.Hour), Route: map[string]route.Answer{
			"category": choiceAns("category", "work"), "topic": choiceAns("topic", "project_work")}},
	}
	svc, fs := newService(t, rows, false)
	first, err := svc.ranking(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	fs.routeGen = 1
	second, err := svc.ranking(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// A commit must not block the first read behind a full rebuild: the
	// current snapshot is served while the new route rows are ranked.
	if second != first {
		t.Fatal("a route generation move blocked instead of serving the snapshot")
	}
	refreshed := waitPriorityIdle(t, svc)
	if refreshed == nil || refreshed.routeGen != 1 {
		t.Fatalf("the rebuilt ranking records route generation %v, want 1",
			refreshed == nil)
	}
}

// The ranking reads the profile's role map, so a renamed reply question still
// produces the reply signal instead of a silent zero.
func TestRankPageReportsTheSnapshotBuildTime(t *testing.T) {
	row := store.PriorityRow{MessageID: 1, Subject: "s", ReceivedAt: receivedAgo(0),
		Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)}}
	cfg := configtest.New(t, nil)
	clk := &fakeClock{now: testNow}
	svc, err := New(cfg, Deps{Store: &fakeStore{rows: []store.PriorityRow{row}}, Gate: allowGate{},
		Config: cfg, Clock: clk, RouteKey: testRouteKey})
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc.RankPage(t.Context(), FilterAll, 5, 0)
	if err != nil || first.GeneratedAt.IsZero() {
		t.Fatalf("first page: %+v %v", first, err)
	}
	clk.Advance(maxCacheTTL + time.Second)
	second, err := svc.RankPage(t.Context(), FilterAll, 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !second.GeneratedAt.Equal(first.GeneratedAt) {
		t.Fatalf("a stale serve moved generated_at: %v -> %v", first.GeneratedAt, second.GeneratedAt)
	}
	waitPriorityIdle(t, svc)
}

// TestThreadStatesConcurrentReadsAcrossRebuilds is the race contract for the
// shared conversation-state map. Consumers (agenda, world, conversations, QA,
// the thread API) read the cached map directly while a generation change
// rebuilds it behind the request; the map must stay immutable and the
// per-build scan state must never leak into the snapshot they hold.
func TestThreadStatesConcurrentReadsAcrossRebuilds(t *testing.T) {
	rows := []store.PriorityRow{{MessageID: 1, ConversationID: "C1", Subject: "s", ReceivedAt: receivedAgo(0),
		Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)}}}
	states := map[string]threadintel.Assessment{"C1": {State: threadintel.AwaitingOwner, LastMessageID: 1, ReplyToMessageID: 1}}
	svc, fs, _ := newThreadService(t, rows, states)
	if _, err := svc.ThreadStates(t.Context()); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				got, err := svc.ThreadStates(context.Background())
				if err != nil {
					t.Errorf("thread states during a rebuild: %v", err)
					return
				}
				if len(got) != 1 || got["C1"].State != threadintel.AwaitingOwner {
					t.Errorf("thread states during a rebuild = %+v", got)
					return
				}
			}
		}()
	}

	for i := int64(2); i < 8; i++ {
		fs.mu.Lock()
		fs.mailGen++
		fs.rows = append(fs.rows, store.PriorityRow{MessageID: i, ConversationID: "C1", Subject: "s",
			ReceivedAt: receivedAgo(0), Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)}})
		fs.mu.Unlock()
		if _, _, _, err := svc.Rank(t.Context(), FilterAll, 8); err != nil {
			t.Fatal(err)
		}
		waitPriorityIdle(t, svc)
	}
	close(stop)
	wg.Wait()
}
