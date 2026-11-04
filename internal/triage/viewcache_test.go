package triage

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// --- the triage views -----------------------------------------------------

// cachedWorld commits a route over three messages and wires the route.
func cachedWorld(t *testing.T) (*world, *taxonomy.Profile, []int64) {
	t.Helper()
	w := newWorld(t, nil)
	p, err := taxonomy.Load(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	var rows []store.RouteAnswer
	for i := range 3 {
		id := w.mail("a@client.com", "A", "Budget", time.Duration(i)*time.Hour, 0)
		ids = append(ids, id)
		body, err := json.Marshal(routeAnswerSet(p, 0))
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, store.RouteAnswer{MessageID: id, Revision: w.revision(t, id), Answers: string(body)})
	}
	commitRoutes(t, w, rows)
	w.useRoute(t, w.st)
	return w, p, ids
}

// A routed page and summary are served from the cache, and a layer-1 commit
// (which bumps only the route generation) invalidates them while the mailbox
// generation stays put.
func TestTriagePageAndSummaryCacheFollowARouteCommit(t *testing.T) {
	w, p, ids := cachedWorld(t)
	gen := w.st.Generation()

	page, err := w.tr.Page(t.Context(), PageQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	hit, err := w.tr.Page(t.Context(), PageQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if hit != page {
		t.Fatal("a repeated page was rebuilt instead of served from the cache")
	}
	if def, err := w.tr.Page(t.Context(), PageQuery{Limit: TriagePageDefault}); err != nil || def == page {
		t.Fatalf("a different page size reused the cached page: %v %v", def == page, err)
	}
	summary, err := w.tr.Summary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if hit, err := w.tr.Summary(t.Context()); err != nil || hit != summary {
		t.Fatalf("a repeated summary was rebuilt instead of served from the cache: %v", err)
	}

	// A new route commit changes one message's category and keeps the mail
	// (and therefore the triage core) untouched.
	newest := ids[len(ids)-1]
	changed := routeAnswerSet(p, 1)
	var rows []store.RouteAnswer
	for _, id := range ids {
		set := routeAnswerSet(p, 0)
		if id == newest {
			set = changed
		}
		body, err := json.Marshal(set)
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, store.RouteAnswer{MessageID: id, Revision: w.revision(t, id), Answers: string(body)})
	}
	commitRoutes(t, w, rows)
	if got := w.st.Generation(); got != gen {
		t.Fatalf("the route commit moved the mailbox generation: %d → %d", gen, got)
	}

	after, err := w.tr.Page(t.Context(), PageQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if after == page {
		t.Fatal("a route commit left the cached page in place")
	}
	want := changed["category"].Choice
	detail, err := w.tr.Result(t.Context(), newest)
	if err != nil {
		t.Fatal(err)
	}
	if got := detail.Values["category"].Choice; got != want {
		t.Fatalf("the page serves %q, want the committed %q", got, want)
	}
	if after.Results[newest].FirstQuestion == "" {
		t.Fatalf("the cached page row lost its first value: %+v", after.Results[newest])
	}
	afterSummary, err := w.tr.Summary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if afterSummary == summary {
		t.Fatal("a route commit left the cached summary in place")
	}
	if afterSummary.Total != summary.Total {
		t.Fatalf("the summary population changed: %d → %d", summary.Total, afterSummary.Total)
	}
}

// A closed gate never serves a cached view.
func TestTriageCacheRespectsTheGate(t *testing.T) {
	w, _, _ := cachedWorld(t)
	g := &testGate{}
	g.open.Store(true)
	tr, err := New(w.cfg, w.st, w.bus, w.src, g, clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	tr.UseRoute(w.st, w.routeKeyer(t))
	if _, err := tr.Page(t.Context(), PageQuery{Limit: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Summary(t.Context()); err != nil {
		t.Fatal(err)
	}
	g.open.Store(false)
	if _, err := tr.Page(t.Context(), PageQuery{Limit: 2}); !errors.Is(err, errTestGate) {
		t.Fatalf("a closed gate served the cached page: %v", err)
	}
	if _, err := tr.Summary(t.Context()); !errors.Is(err, errTestGate) {
		t.Fatalf("a closed gate served the cached summary: %v", err)
	}
	// Reopening the gate serves the same snapshots: the gate is checked,
	// never cached.
	g.open.Store(true)
	if _, err := tr.Page(t.Context(), PageQuery{Limit: 2}); err != nil {
		t.Fatal(err)
	}
}

// The explicit test preview is part of the key: toggling it builds another
// snapshot instead of serving the other mode's view.
func TestTriageCacheSeparatesTheTestPreview(t *testing.T) {
	w, _, _ := cachedWorld(t)
	g := &testGate{}
	g.open.Store(true)
	tr, err := New(w.cfg, w.st, w.bus, w.src, g, clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	tr.UseRoute(w.st, w.routeKeyer(t))
	plain, err := tr.Page(t.Context(), PageQuery{Limit: 2})
	if err != nil || plain.Preview {
		t.Fatalf("plain page %+v %v", plain, err)
	}
	// The setting alone must not turn a certified page into a preview: the
	// real gate is open, so the certified answers win and the cache keeps
	// serving the same snapshot.
	g.preview.Store(true)
	stillPlain, err := tr.Page(t.Context(), PageQuery{Limit: 2})
	if err != nil || stillPlain.Preview {
		t.Fatalf("preview setting relabelled a certified page: %+v %v", stillPlain, err)
	}
	if stillPlain != plain {
		t.Fatal("the certified page was rebuilt when the preview setting changed")
	}
	// Once the real gate closes, the explicit preview is the only way to
	// read the screen, and it is marked as such.
	g.open.Store(false)
	preview, err := tr.Page(t.Context(), PageQuery{Limit: 2})
	if err != nil || !preview.Preview {
		t.Fatalf("preview page %+v %v", preview, err)
	}
	if preview == plain {
		t.Fatal("the preview page reused the non-preview snapshot")
	}
	again, err := tr.Page(t.Context(), PageQuery{Limit: 2})
	if err != nil || again != preview {
		t.Fatalf("the preview page was rebuilt: %v", err)
	}
}

// The config version is part of the key: a settings change that alters the
// rendered page (here the triaged folder roles) is not served from the old
// snapshot.
func TestTriageCacheFollowsSettings(t *testing.T) {
	w, _, _ := cachedWorld(t)
	page, err := w.tr.Page(t.Context(), PageQuery{Limit: 2})
	if err != nil || page.Total != 3 {
		t.Fatalf("page %+v %v", page, err)
	}
	if err := w.cfg.Set("triage.folder_roles", []any{"sent"}); err != nil {
		t.Fatal(err)
	}
	after, err := w.tr.Page(t.Context(), PageQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if after == page {
		t.Fatal("a settings change left the cached page in place")
	}
	if after.Total != 0 {
		t.Fatalf("the page still counts %d inbox messages after the roles changed", after.Total)
	}
}

// concurrentPageRoute counts the builds behind one cold page and blocks them
// so every reader of the missing key is in flight together.
type concurrentPageRoute struct {
	RouteReader
	calls   atomic.Int64
	started chan struct{}
	release chan struct{}
}

func (r *concurrentPageRoute) LoadRouteRecords(ctx context.Context, routeKey string, ids []int64) (map[int64]store.RouteRecord, error) {
	r.calls.Add(1)
	r.started <- struct{}{}
	<-r.release
	return r.RouteReader.LoadRouteRecords(ctx, routeKey, ids)
}

// Eight concurrent cold page readers share one build: the route is read once.
func TestPageColdBuildIsSingleFlight(t *testing.T) {
	w, _, _ := cachedWorld(t)
	route := &concurrentPageRoute{RouteReader: w.st, started: make(chan struct{}, 8), release: make(chan struct{})}
	w.useRoute(t, route)

	const n = 8
	entered := make(chan struct{}, n)
	pages := make([]*Page, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entered <- struct{}{}
			pages[i], errs[i] = w.tr.Page(context.Background(), PageQuery{Limit: 2})
		}()
	}
	for range n {
		<-entered
	}
	<-route.started
	close(route.release)
	wg.Wait()

	if got := route.calls.Load(); got != 1 {
		t.Fatalf("a cold page read the route %d times, want 1", got)
	}
	for i := range n {
		if errs[i] != nil || pages[i] == nil || pages[i].Total != 3 {
			t.Fatalf("caller %d: %+v %v", i, pages[i], errs[i])
		}
	}
}

// errTestGate is the closed gate's error.
var errTestGate = errors.New("triage: learning is not done")

// testGate is the learning gate and the explicit test preview, switchable
// in a test.
type testGate struct {
	open    atomic.Bool
	preview atomic.Bool
}

func (g *testGate) Allow(context.Context) error {
	if !g.open.Load() && !g.preview.Load() {
		return errTestGate
	}
	return nil
}

func (g *testGate) PreviewAnswers(context.Context) bool { return g.preview.Load() && !g.open.Load() }
