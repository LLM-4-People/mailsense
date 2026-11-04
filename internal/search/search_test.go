package search

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/engine/embed"
	"github.com/LLM-4-People/mailsense/internal/engine/embed/embedtest"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/index"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/learning/learningtest"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/route"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/secrets"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
	"github.com/LLM-4-People/mailsense/internal/vector"
)

type fixedLocator struct{ url string }

func (l fixedLocator) Locate(context.Context, string, string) (engine.Endpoint, error) {
	return engine.Endpoint{URL: l.url}, nil
}

type world struct {
	t    *testing.T
	cfg  *config.Config
	st   *store.Store
	bus  *events.Bus
	fake *embedtest.Server
	ix   *index.Indexer
	s    *Searcher
	ids  map[string]int64
}

func newWorld(t *testing.T, tr TriageReader) *world {
	t.Helper()
	return newWorldWith(t, tr, nil)
}

func newWorldWith(t *testing.T, tr TriageReader, overrides map[string]any) *world {
	t.Helper()
	w := &world{t: t, bus: events.New(), ids: map[string]int64{}}
	t.Cleanup(w.bus.Close)
	settings := map[string]any{"http.retry.max_attempts": 1, source.SettingSelected: []any{source.StoreKey("src", "store-1")}}
	for key, value := range overrides {
		settings[key] = value
	}
	w.cfg = configtest.New(t, settings)
	st, err := store.Open(t.Context(), w.cfg, w.bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	w.st = st
	_ = st.UpsertSource(t.Context(), "src", "test", "Test", source.Capabilities{})
	w.fake = embedtest.New(t, &embedtest.Fake{})
	embedtest.Install(t, w.cfg, w.fake.URL)
	sec, _ := secrets.Open(w.cfg, nil, nil, nil)
	src := embed.NewSource(engine.Deps{Config: w.cfg, Bus: w.bus, Secrets: sec, Locator: embedtest.Locator{URL: w.fake.URL}})
	w.ix = index.New(w.cfg, st, w.bus, src, nil)
	w.s = newSearcher(t, w.cfg, Deps{Store: st, Embedder: src, Coverage: w.ix, Triage: tr, Gate: learningtest.Open{}, Bus: w.bus})
	return w
}

func (w *world) mail(id string, role mail.FolderRole, subject, body string) {
	w.t.Helper()
	m := mailtest.Message("src", id)
	m.FolderRole, m.FolderPath, m.Subject, m.Body, m.SentOn = role, string(role), subject, body, ""
	m.ReceivedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(len(w.ids)) * time.Hour).Format(mail.TimeLayout)
	w.put(m)
}

func (w *world) put(m mail.Message) {
	w.t.Helper()
	if err := w.st.UpsertMessages(w.t.Context(), 0, []mail.Message{m}); err != nil {
		w.t.Fatal(err)
	}
	ids, _ := w.st.FilterIDs(w.t.Context(), store.SearchFilter{})
	for _, id := range ids {
		if got, _ := w.st.MessageByRow(w.t.Context(), id); got.ID == m.ID {
			w.ids[m.ID] = id
		}
	}
}

func (w *world) index() {
	w.t.Helper()
	if _, err := w.ix.Run(w.t.Context(), index.Options{}, nil); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) search(q Query) Result {
	w.t.Helper()
	res, err := w.s.Search(w.t.Context(), q)
	if err != nil {
		w.t.Fatal(err)
	}
	return res
}

func (w *world) order(res Result) []string {
	var out []string
	for _, h := range res.Hits {
		out = append(out, h.ID)
	}
	return out
}

func seedThree(w *world) {
	w.mail("both", mail.RoleInbox, "Invoice for the budget", "Please find the budget invoice attached.")
	w.mail("vec", mail.RoleSent, "Re: invoice", "The invoice was paid yesterday.")
	w.mail("none", mail.RoleInbox, "Lunch", "Pizza on friday?")
}

func TestHybridSearchFusesBothRankings(t *testing.T) {
	w := newWorld(t, nil)
	seedThree(w)
	w.index()
	res := w.search(Query{Text: "budget invoice"})
	if !res.Semantic.Used || res.Semantic.Covered != 3 || res.Semantic.Eligible != 3 || res.TextMatches != 1 {
		t.Fatalf("semantic used, full coverage, one text match: %+v", res)
	}
	if got := w.order(res); len(got) < 2 || got[0] != "both" || got[1] != "vec" {
		t.Fatalf("the message both rankings found comes first, then the similar one: %v", got)
	}
	top := res.Hits[0]
	if top.TextRank != 1 || top.VectorRank != 1 || top.Score <= res.Hits[1].Score || res.Hits[1].TextRank != 0 {
		t.Fatalf("ranks and fused scores: %+v / %+v", top, res.Hits[1])
	}
	if res.Notice != "semantic index covers 3 of 3 messages" {
		t.Fatalf("notice %q", res.Notice)
	}
	if !strings.Contains(top.Snippet, "budget invoice") || top.Subject != "Invoice for the budget" || top.Role != mail.RoleInbox {
		t.Fatalf("hit summary and snippet: %+v", top)
	}
}

func TestSearchDegradesToFullTextAndSaysWhy(t *testing.T) {
	w := newWorld(t, nil)
	seedThree(w)
	w.index()
	sub, _ := events.Subscribe(w.bus, TopicStatus, 8)
	w.fake.Fake.SetStatus(http.StatusInternalServerError)
	res := w.search(Query{Text: "budget invoice"})
	if res.Semantic.Used || res.Semantic.Reason == "" || len(res.Hits) != 1 || res.Hits[0].ID != "both" {
		t.Fatalf("full text only: %+v", res)
	}
	if !strings.HasPrefix(res.Notice, "full-text results only (semantic search unavailable: ") || !strings.HasSuffix(res.Notice, "covers 3 of 3 messages") {
		t.Fatalf("notice %q", res.Notice)
	}
	w.fake.Fake.SetStatus(0)
	w.search(Query{Text: "budget invoice"})
	w.search(Query{Text: "budget invoice"})
	if a, b := <-sub.C, <-sub.C; a.Available || !b.Available || len(sub.C) != 0 {
		t.Fatalf("one event per change: %+v %+v (+%d)", a, b, len(sub.C))
	}
}

func TestSlowEmbedderTimesOut(t *testing.T) {
	w := newWorld(t, nil)
	seedThree(w)
	w.index()
	w.fake.Fake.Hang = true // never answers: the 150 ms default budget guards a call that cannot succeed
	start := time.Now()
	res := w.search(Query{Text: "invoice"})
	elapsed := time.Since(start)
	if elapsed > time.Second {
		t.Fatalf("a slow embedder held the full-text answer for %s", elapsed)
	}
	if res.Semantic.Used || !res.Semantic.TimedOut || !strings.Contains(res.Semantic.Reason, "semantic budget (150ms)") || len(res.Hits) != 2 {
		t.Fatalf("degraded after the 150 ms budget: %+v", res.Semantic)
	}
	if !strings.HasPrefix(res.Notice, "full-text results only (the ") || !strings.HasSuffix(res.Notice, "covers 3 of 3 messages") {
		t.Fatalf("the degradation is silent: %q", res.Notice)
	}
}

func TestWaitingSearchOutlastsTheInteractiveBudget(t *testing.T) {
	w := newWorld(t, nil)
	seedThree(w)
	w.index()
	// Two model calls share the budget: each request is one input, and the
	// fake's delay is per input. 300 ms is double the 150 ms interactive
	// budget, so only a waiting search can finish.
	w.fake.Fake.Delay = 300 * time.Millisecond
	start := time.Now()
	res := w.search(Query{Text: "invoice", Wait: true})
	elapsed := time.Since(start)
	if !res.Semantic.Used || res.Semantic.TimedOut {
		t.Fatalf("a waiting search did not finish the semantic leg: %+v", res.Semantic)
	}
	if elapsed < 300*time.Millisecond {
		t.Fatalf("a waiting search returned before the embedder answered (%s)", elapsed)
	}
	if len(res.Hits) == 0 {
		t.Fatal("a waiting search returned no hits")
	}
	// The interactive path keeps its budget: a different slow query (the
	// first text is now cached) degrades instead of holding a typed query.
	res = w.search(Query{Text: "invoice budget"})
	if res.Semantic.Used || !res.Semantic.TimedOut {
		t.Fatalf("the interactive search lost its budget: %+v", res.Semantic)
	}
}

func TestEmptyMailboxDoesNotStartTheEmbedder(t *testing.T) {
	w := newWorld(t, nil)
	before := w.fake.Fake.Calls()
	res := w.search(Query{Text: "invoice", Wait: true})
	if !res.Semantic.Used || res.Semantic.Eligible != 0 || res.Semantic.Covered != 0 {
		t.Fatalf("empty mailbox semantic state = %+v", res.Semantic)
	}
	if got := w.fake.Fake.Calls(); got != before {
		t.Fatalf("an empty mailbox embedded a query (%d -> %d calls)", before, got)
	}
	if len(res.Hits) != 0 {
		t.Fatalf("empty mailbox hits = %v", w.order(res))
	}
}

func TestFiltersNarrowBothRankings(t *testing.T) {
	w := newWorld(t, nil)
	seedThree(w)
	w.index()
	res := w.search(Query{Text: "budget invoice", Filter: store.SearchFilter{Roles: []mail.FolderRole{mail.RoleInbox}}})
	for _, h := range res.Hits {
		if h.Role != mail.RoleInbox {
			t.Fatalf("a sent message passed the inbox filter: %v", w.order(res))
		}
	}
	res = w.search(Query{Text: "budget invoice", Filter: store.SearchFilter{Roles: []mail.FolderRole{mail.RoleSent}}, Limit: 1})
	if got := w.order(res); fmt.Sprint(got) != "[vec]" || res.TextMatches != 0 {
		t.Fatalf("only the similar sent message: %v", got)
	}
}

type fakeTriage map[int64]triage.Result

func (f fakeTriage) Results(_ context.Context, rows []int64) (map[int64]triage.Result, error) {
	out := map[int64]triage.Result{}
	for _, r := range rows {
		if v, ok := f[r]; ok {
			out[r] = v
		}
	}
	return out, nil
}

func TestTriageFilters(t *testing.T) {
	tr := fakeTriage{}
	w := newWorld(t, tr)
	seedThree(w)
	w.index()
	tr[w.ids["both"]] = triage.Result{Values: map[string]taxonomy.Value{
		"category": {Type: engine.Choice, Choice: "action", Abstain: route.AbstainNotCertified},
		"urgency":  {Type: engine.Score, Number: 3},
		"effort":   {Type: engine.Score, Number: 3, Abstain: route.AbstainNotCertified}}}
	tr[w.ids["vec"]] = triage.Result{Values: map[string]taxonomy.Value{
		"category": {Type: engine.Choice, Choice: "work"},
		"urgency":  {Type: engine.Score, Number: 1, Refused: true}}}
	two := 2.0
	cases := map[string]struct {
		f    []TriageFilter
		want string
	}{
		"choice":           {[]TriageFilter{{Question: "category", Choices: []string{"work"}}}, "[vec]"},
		"abstained choice": {[]TriageFilter{{Question: "category", Choices: []string{"action"}}}, "[]"},
		"abstained score":  {[]TriageFilter{{Question: "effort", Min: &two}}, "[]"},
		"min":              {[]TriageFilter{{Question: "urgency", Min: &two}}, "[both]"},
		"refused fail":     {[]TriageFilter{{Question: "urgency", Max: &two}}, "[]"},
		"unknown":          {[]TriageFilter{{Question: "nope", Choices: []string{"x"}}}, "[]"},
	}
	for name, c := range cases {
		if got := fmt.Sprint(w.order(w.search(Query{Text: "invoice", Triage: c.f}))); got != c.want {
			t.Errorf("%s: %s, want %s", name, got, c.want)
		}
	}
	if _, err := w.s.Search(t.Context(), Query{Text: "x", Triage: []TriageFilter{{Question: "category"}}}); err == nil {
		t.Fatal("a filter without a condition is refused")
	}
	bare := newWorld(t, nil)
	if _, err := bare.s.Search(t.Context(), Query{Text: "x", Triage: []TriageFilter{{Question: "c", Choices: []string{"a"}}}}); !errors.Is(err, ErrNoTriage) {
		t.Fatalf("no triage wired: %v", err)
	}
	if _, err := bare.s.Search(t.Context(), Query{Text: "  "}); !errors.Is(err, ErrEmptyQuery) {
		t.Fatal("empty query")
	}
}

func TestVectorsLoadIncrementallyAndFollowRemovals(t *testing.T) {
	w := newWorld(t, nil)
	seedThree(w)
	w.index()
	w.search(Query{Text: "invoice"})
	seq := w.s.vectors.Seq()
	w.mail("new", mail.RoleInbox, "Quarterly invoice", "another invoice")
	w.index()
	res := w.search(Query{Text: "quarterly"})
	if w.s.vectors.Len() != 4 || w.s.vectors.Seq() <= seq || res.Semantic.Covered != 4 {
		t.Fatalf("the new vector is loaded: %d (seq %d → %d) %+v", w.s.vectors.Len(), seq, w.s.vectors.Seq(), res.Semantic)
	}
	if _, err := w.st.RemoveMessages(t.Context(), []int64{w.ids["new"], w.ids["none"]}); err != nil {
		t.Fatal(err)
	}
	res = w.search(Query{Text: "invoice"})
	if w.s.vectors.Len() != 2 || res.Semantic.Covered != 2 || res.Semantic.Eligible != 2 {
		t.Fatalf("removed messages leave the copy: %d %+v", w.s.vectors.Len(), res.Semantic)
	}
}

func TestSnippet(t *testing.T) {
	body := strings.Repeat("filler words here ", 20) + "la Réunion de budget est jeudi " + strings.Repeat("more text after ", 20)
	plan := snippetPlan([]store.TermMatch{{Term: "reunion", Mode: store.ModeWord}})
	s, ok := snippet(body, plan, 60)
	if !ok || !strings.Contains(s, "Réunion de budget") || !strings.HasPrefix(s, ellipsis) || !strings.HasSuffix(s, ellipsis) || len([]rune(s)) > 62 {
		t.Fatalf("around the accent-insensitive match: %q (ok %v)", s, ok)
	}
	if s, ok := snippet("Hello there.\n\n> quoted\n-- \nsig", snippetPlan(nil), 100); ok || s != "" {
		t.Fatalf("a full-text hit without an anchor is empty, not the body beginning: %q (ok %v)", s, ok)
	}
	if s := preview("Hello there.\n\n> quoted\n-- \nsig", 100); s != "Hello there." {
		t.Fatalf("the cleaned preview: %q", s)
	}
	if s, ok := snippet("short", snippetPlan([]store.TermMatch{{Term: "short", Mode: store.ModeWord}}), 100); !ok || s != "short" {
		t.Fatalf("nothing cut: %q (ok %v)", s, ok)
	}
}

func TestFuseIsReciprocalRankFusion(t *testing.T) {
	got := fuse([]int64{1, 2}, []vector.Scored{{ID: 3, Sim: 0.9}, {ID: 1, Sim: 0.8}}, 60)
	if len(got) != 3 || got[0].id != 1 || math.Abs(got[0].score-(1.0/61+1.0/62)) > 1e-12 || got[1].id != 3 || got[2].id != 2 {
		t.Fatalf("%+v", got)
	}
	if fuse(nil, nil, 60) == nil || len(fuse(nil, nil, 60)) != 0 {
		t.Fatal("empty")
	}
}

// Review 2b #1 (failed first: the new message's vector was never loaded):
// the vector with the highest write sequence is removed, a new message is
// indexed; with MAX(seq)+1 it took the removed one's number, the
// incremental load past it never saw it and the count looked unchanged.
func TestANewVectorAfterARemovalIsFound(t *testing.T) {
	w := newWorld(t, nil)
	seedThree(w)
	w.index()
	w.search(Query{Text: "invoice"})
	vs, _ := w.st.LoadVectors(t.Context(), w.s.vectors.Space(), 0)
	last := vs[len(vs)-1]
	if _, err := w.st.RemoveMessages(t.Context(), []int64{last.MessageID}); err != nil {
		t.Fatal(err)
	}
	w.mail("new", mail.RoleInbox, "Quarterly zebra", "zebra stripes")
	w.index()
	w.search(Query{Text: "quarterly zebra"})
	found, stale := w.s.vectors.Has(w.ids["new"]), w.s.vectors.Has(last.MessageID)
	if !found || stale || w.s.vectors.Len() != 3 {
		t.Fatalf("the copy has the new vector and not the removed one: new %v removed %v size %d", found, stale, w.s.vectors.Len())
	}
	again, _ := w.st.LoadVectors(t.Context(), w.s.vectors.Space(), last.Seq)
	if len(again) != 1 || again[0].Seq <= last.Seq {
		t.Fatalf("write sequence numbers are never reused: %+v", again)
	}
}

// Review 2b (the reviewer's FTS probe): punctuation in a query never
// breaks full text or search.
func TestPunctuationInQueries(t *testing.T) {
	w := newWorld(t, nil)
	seedThree(w)
	w.index()
	for _, q := range []string{"budget - invoice", "budget & invoice", "budget:", "invoice ?"} {
		if res := w.search(Query{Text: q}); res.TextMatches == 0 || len(res.Hits) == 0 {
			t.Errorf("%q: %+v", q, res)
		}
	}
	for _, q := range []string{"—", "*", "NEAR(budget", "c++", `\`} {
		if _, err := w.s.Search(t.Context(), Query{Text: q}); err != nil {
			t.Errorf("%q: %v", q, err)
		}
	}
}

// User decision 2026-10-15: search answers nothing until the picked
// mailboxes are fully learned; a searcher wired without a gate refuses.
// The Q&A owner-only option narrows both rankings through the same filter;
// the default `/api/search` behavior still includes shared mail.
func TestOwnerOnlyNarrowsBothRankings(t *testing.T) {
	w := newWorld(t, nil)
	seedThree(w)
	w.mail("unknown", mail.RoleInbox, "Budget note", "budget invoice unattributed")
	for _, id := range []int64{w.ids["both"], w.ids["none"]} {
		if err := w.st.WriteVerdicts(t.Context(), "test", []store.VerdictRow{{RowID: id,
			Verdict: scope.Owner, Reason: scope.ReasonAddressedFlag}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.st.WriteVerdicts(t.Context(), "test", []store.VerdictRow{{RowID: w.ids["vec"],
		Verdict: scope.Shared, Reason: scope.ReasonNotAddressed}}); err != nil {
		t.Fatal(err)
	}
	w.index()
	owner := w.search(Query{Text: "budget invoice", Filter: store.SearchFilter{OwnerOnly: true}})
	if !owner.Semantic.Used || owner.Semantic.Covered != 4 {
		t.Fatalf("the semantic path must run under the owner filter: %+v", owner.Semantic)
	}
	if owner.TextMatches != 1 {
		t.Fatalf("the full-text path must use the owner filter too: %d matches", owner.TextMatches)
	}
	for _, h := range owner.Hits {
		if h.ID == "vec" || h.ID == "unknown" {
			t.Fatalf("shared/unattributed mail leaked through owner-only search: %+v", h.Summary)
		}
	}
	if len(owner.Hits) == 0 || owner.Hits[0].ID != "both" {
		t.Fatalf("owner-only hits %v", w.order(owner))
	}
	all := w.search(Query{Text: "budget invoice"})
	if !slices.Contains(w.order(all), "vec") || !slices.Contains(w.order(all), "unknown") {
		t.Fatalf("the default search must keep shared and unattributed mail: %v", w.order(all))
	}
	ids, err := w.st.FilterIDs(t.Context(), store.SearchFilter{OwnerOnly: true})
	if err != nil || slices.Contains(ids, w.ids["vec"]) || slices.Contains(ids, w.ids["unknown"]) ||
		!slices.Contains(ids, w.ids["both"]) {
		t.Fatalf("owner filter ids %v: %v", ids, err)
	}
}

func TestSearchIsGated(t *testing.T) {
	w := newWorld(t, nil)
	w.mail("x", mail.RoleInbox, "Invoice", "Please find the invoice.")
	locked := lockedGate{}
	w.s.gate = locked
	if _, err := w.s.Search(t.Context(), Query{Text: "invoice"}); !errors.Is(err, learning.ErrLocked) {
		t.Fatalf("a closed gate refuses: %v", err)
	}
	// Review L0 rules §5 (failed first: New accepted a nil gate and the
	// refusal came only at the first query): refused at construction.
	if s, err := New(w.cfg, Deps{Store: w.st}); !errors.Is(err, ErrNoGate) || s != nil {
		t.Fatalf("no gate, no searcher: %v", err)
	}
}

type lockedGate struct{}

func (lockedGate) Allow(context.Context) error { return &learning.LockedError{} }

// countingEmbedder wraps the configured embedder to count model calls and,
// when gate is non-nil, hold them until the test releases the gate.
type countingEmbedder struct {
	inner engine.Embedder
	mu    sync.Mutex
	calls int
	gate  chan struct{}
}

func (e *countingEmbedder) Name() string               { return e.inner.Name() }
func (e *countingEmbedder) Model() string              { return e.inner.Model() }
func (e *countingEmbedder) Space() string              { return e.inner.Space() }
func (e *countingEmbedder) Dimensions() int            { return e.inner.Dimensions() }
func (e *countingEmbedder) Input(t engine.Text) string { return e.inner.Input(t) }

func (e *countingEmbedder) Embed(ctx context.Context, texts []engine.Text) (engine.Embeddings, error) {
	e.mu.Lock()
	e.calls++
	gate := e.gate
	e.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return engine.Embeddings{}, ctx.Err()
		}
	}
	return e.inner.Embed(ctx, texts)
}

func (e *countingEmbedder) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

type fixedEmbedderSource struct{ emb engine.Embedder }

func (s fixedEmbedderSource) Embedder() (engine.Embedder, error) { return s.emb, nil }

// TestQueryEmbeddingCacheOneModelCallPerText pins the cache contract: the
// same normalized query is embedded once, an evicted query is embedded
// again, and a text-only suggestion never reaches the embedder.
func TestQueryEmbeddingCacheOneModelCallPerText(t *testing.T) {
	w := newWorld(t, nil)
	seedThree(w)
	w.index()
	inner, err := w.s.src.Embedder()
	if err != nil {
		t.Fatal(err)
	}
	ce := &countingEmbedder{inner: inner}
	w.s.src = fixedEmbedderSource{ce}

	first := w.search(Query{Text: "budget invoice"})
	if !first.Semantic.Used || ce.count() != 1 {
		t.Fatalf("first search: used %v, %d embeds", first.Semantic.Used, ce.count())
	}
	again := w.search(Query{Text: "BUDGET INVOICE"})
	if !again.Semantic.Used || ce.count() != 1 {
		t.Fatalf("a case variant paid a second embed: %d", ce.count())
	}
	if fmt.Sprint(w.order(first)) != fmt.Sprint(w.order(again)) {
		t.Fatalf("the cached query changed the ranking: %v vs %v", w.order(first), w.order(again))
	}

	// Text-only is the omnibox's mode: the same text results, no model call,
	// and an honest disclosure.
	before := ce.count()
	text := w.search(Query{Text: "budget invoice", TextOnly: true})
	if text.Semantic.Used || ce.count() != before {
		t.Fatalf("text-only search used the embedder: used %v, %d calls", text.Semantic.Used, ce.count()-before)
	}
	// The text-only page is the same full-text ranking: every one of its
	// hits was found by the text leg, and the semantic-only neighbours are
	// absent.
	textOnlyHits := map[string]bool{}
	for _, hit := range text.Hits {
		textOnlyHits[hit.ID] = true
	}
	for _, hit := range first.Hits {
		if hit.TextRank > 0 && !textOnlyHits[hit.ID] {
			t.Fatalf("text-only page dropped text hit %q: %v", hit.ID, w.order(text))
		}
		if hit.TextRank == 0 && textOnlyHits[hit.ID] {
			t.Fatalf("text-only page kept semantic-only hit %q", hit.ID)
		}
	}
	if !strings.Contains(text.Notice, "semantic ranking was not requested") ||
		strings.Contains(text.Notice, "unavailable") {
		t.Fatalf("text-only notice is not honest: %q", text.Notice)
	}

	// Fill the bound with distinct texts, then return to the first: an
	// evicted text is embedded again.
	for i := range queryCacheSize {
		w.search(Query{Text: fmt.Sprintf("budget %d", i)})
	}
	if got := ce.count(); got != queryCacheSize+1 {
		t.Fatalf("%d embeds for %d distinct texts (plus the first)", got, queryCacheSize)
	}
	w.search(Query{Text: "budget invoice"})
	if got := ce.count(); got != queryCacheSize+2 {
		t.Fatalf("an evicted query was not embedded again: %d calls", got)
	}
}

func TestQueryEmbeddingCacheSingleFlight(t *testing.T) {
	w := newWorld(t, nil)
	seedThree(w)
	w.index()
	inner, err := w.s.src.Embedder()
	if err != nil {
		t.Fatal(err)
	}
	ce := &countingEmbedder{inner: inner, gate: make(chan struct{})}
	w.s.src = fixedEmbedderSource{ce}

	done := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := w.s.Search(context.Background(), Query{Text: "budget invoice", Wait: true})
			done <- err
		}()
	}
	deadline := time.Now().Add(2 * time.Second)
	for ce.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	// Both searches have now reached the shared cache entry; a broken
	// single-flight would have started a second model call.
	time.Sleep(50 * time.Millisecond)
	close(ce.gate)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if got := ce.count(); got != 1 {
		t.Fatalf("concurrent identical searches embedded %d times", got)
	}
}

func TestQueryEmbeddingFailureIsNotCached(t *testing.T) {
	w := newWorld(t, nil)
	seedThree(w)
	w.index()
	inner, err := w.s.src.Embedder()
	if err != nil {
		t.Fatal(err)
	}
	ce := &countingEmbedder{inner: inner}
	w.s.src = fixedEmbedderSource{ce}

	w.fake.Fake.SetStatus(http.StatusInternalServerError)
	defer w.fake.Fake.SetStatus(0)
	failed := w.search(Query{Text: "budget invoice"})
	if failed.Semantic.Used || ce.count() != 1 {
		t.Fatalf("failed embed: used %v, %d calls", failed.Semantic.Used, ce.count())
	}
	w.fake.Fake.SetStatus(0)
	recovered := w.search(Query{Text: "budget invoice"})
	if !recovered.Semantic.Used || ce.count() != 2 {
		t.Fatalf("the failed call was cached: used %v, %d calls", recovered.Semantic.Used, ce.count())
	}
	w.search(Query{Text: "budget invoice"})
	if got := ce.count(); got != 2 {
		t.Fatalf("the successful call was not cached: %d calls", got)
	}
}

// newSearcher builds a searcher or fails the test.
func newSearcher(t testing.TB, cfg *config.Config, d Deps) *Searcher {
	t.Helper()
	s, err := New(cfg, d)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSearchTotalOffsetAndCapped(t *testing.T) {
	w := newWorld(t, nil)
	seedThree(w)
	w.index()
	full := w.search(Query{Text: "budget invoice", Limit: 3})
	if full.Capped || full.Total != len(full.Hits) || full.Total != 3 {
		t.Fatalf("full page: total %d capped %v hits %v", full.Total, full.Capped, w.order(full))
	}
	page := w.search(Query{Text: "budget invoice", Limit: 2, Offset: 1})
	want := w.order(full)[1:]
	if got := w.order(page); fmt.Sprint(got) != fmt.Sprint(want) || page.Total != full.Total || page.Capped {
		t.Fatalf("offset page %v (total %d capped %v), want %v", got, page.Total, page.Capped, want)
	}
	if beyond := w.search(Query{Text: "budget invoice", Limit: 2, Offset: 100}); len(beyond.Hits) != 0 || beyond.Total != full.Total {
		t.Fatalf("offset beyond the candidates: %+v", beyond)
	}

	if err := w.cfg.Set("search.max_hits", 1); err != nil {
		t.Fatal(err)
	}
	small := w.search(Query{Text: "invoice", Limit: 1})
	if !small.Capped {
		t.Fatalf("a ranking cut at search.max_hits must report capped: %+v", small)
	}
}

func TestSearchRejectsOutOfBoundsLimitAndOffset(t *testing.T) {
	w := newWorld(t, nil)
	seedThree(w)
	for _, tc := range []struct {
		name string
		q    Query
		want error
	}{
		{"negative offset", Query{Text: "invoice", Offset: -1}, ErrInvalidOffset},
		{"huge offset", Query{Text: "invoice", Offset: MaxOffset + 1}, ErrInvalidOffset},
		{"negative limit", Query{Text: "invoice", Limit: -1}, ErrInvalidLimit},
		{"limit above max hits", Query{Text: "invoice", Limit: 201}, ErrInvalidLimit},
	} {
		if _, err := w.s.Search(t.Context(), tc.q); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}

func TestSearchOwnerScopeExcludesSharedAndDelegateContent(t *testing.T) {
	w := newWorldWith(t, nil, map[string]any{
		source.SettingSelected: mailtest.Picked([]string{"src"}, [2]string{"src", "store-2"}, [2]string{"src", "store-3"}),
	})
	ctx := t.Context()
	for _, store := range []struct {
		id   string
		kind mail.StoreKind
	}{{"store-2", mail.StoreShared}, {"store-3", mail.StoreDelegate}} {
		if _, err := w.st.UpsertFolder(ctx, mail.Folder{SourceID: "src", StoreID: store.id, StoreName: store.id,
			Path: "Inbox", Role: mail.RoleInbox, StoreKind: store.kind}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"shared", "delegate"} {
		m := mailtest.Message("src", id)
		m.StoreID, m.StoreName = "store-2", "store-2"
		if id == "delegate" {
			m.StoreID, m.StoreName = "store-3", "store-3"
		}
		m.Subject, m.Body = "leak subject", "leak body"
		if err := w.st.UpsertMessages(ctx, 0, []mail.Message{m}); err != nil {
			t.Fatal(err)
		}
	}
	ids, _ := w.st.FilterIDs(ctx, store.SearchFilter{})
	for _, id := range ids {
		if got, _ := w.st.MessageByRow(ctx, id); got.ID == "shared" || got.ID == "delegate" {
			w.ids[got.ID] = id
		}
	}
	if w.ids["shared"] == 0 || w.ids["delegate"] == 0 {
		t.Fatalf("rows were not stored: %v", w.ids)
	}
	w.index()

	all := w.search(Query{Text: "leak"})
	got := w.order(all)
	slices.Sort(got)
	if fmt.Sprint(got) != "[delegate shared]" {
		t.Fatalf("default search must keep shared/delegate mail searchable: %v", w.order(all))
	}
	owner := w.search(Query{Text: "leak", Filter: store.SearchFilter{OwnerOnly: true}})
	if len(owner.Hits) != 0 {
		t.Fatalf("owner scope returned shared/delegate content: %v", w.order(owner))
	}
}

func TestColdVectorRefreshDoesNotBlockTheAnswer(t *testing.T) {
	w := newWorld(t, nil)
	seedThree(w)
	w.index()
	release := make(chan struct{})
	finished := make(chan struct{})
	var once sync.Once
	base := w.s.refresh
	w.s.refresh = func(ctx context.Context, src vector.Source, space string, dims int) error {
		<-release
		err := base(ctx, src, space, dims)
		once.Do(func() { close(finished) })
		return err
	}
	start := time.Now()
	res := w.search(Query{Text: "invoice"})
	elapsed := time.Since(start)
	if elapsed > time.Second {
		t.Fatalf("a cold vector refresh held the answer for %s", elapsed)
	}
	if res.Semantic.Used || !res.Semantic.TimedOut || !strings.Contains(res.Semantic.Reason, "semantic budget (150ms)") {
		t.Fatalf("cold refresh did not degrade explicitly: %+v", res.Semantic)
	}
	if len(res.Hits) == 0 || !strings.HasPrefix(res.Notice, "full-text results only (") {
		t.Fatalf("full text must still answer: hits %d notice %q", len(res.Hits), res.Notice)
	}
	if strings.Contains(res.Notice, "0 of 0") {
		t.Fatalf("a cold timeout invented a coverage count: %q", res.Notice)
	}
	// The abandoned worker must finish the preparation instead of being
	// cancelled at the budget: cancelling it made every retry repeat the
	// same cold work and kept a warm index reporting "warming".
	close(release)
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the semantic worker was cancelled at the budget instead of preparing the index")
	}
	again := w.search(Query{Text: "invoice"})
	if !again.Semantic.Used || again.Semantic.TimedOut {
		t.Fatalf("the next search did not reuse the prepared index: %+v", again.Semantic)
	}
}

type countingCoverage struct{ calls int }

func (c *countingCoverage) Eligible(context.Context) ([]int64, error) {
	c.calls++
	return nil, nil
}

func TestCoverageCacheSurvivesLabelWrites(t *testing.T) {
	w := newWorld(t, nil)
	seedThree(w)
	cov := &countingCoverage{}
	w.s.cov = cov
	w.s.mu.Lock()
	w.s.cache = coverageCache{}
	w.s.mu.Unlock()

	if _, _ = w.s.coverage(t.Context()); cov.calls != 1 {
		t.Fatalf("first coverage read called Eligible %d times", cov.calls)
	}
	if err := w.st.PutLabel(t.Context(), w.ids["both"], "category", `"work"`, "manual"); err != nil {
		t.Fatal(err)
	}
	if _, _ = w.s.coverage(t.Context()); cov.calls != 1 {
		t.Fatalf("a label write rebuilt the eligible set (%d calls)", cov.calls)
	}
	w.mail("new", mail.RoleInbox, "New mail", "A new message changes the eligible set.")
	if _, _ = w.s.coverage(t.Context()); cov.calls != 2 {
		t.Fatalf("a new message did not rebuild the eligible set (%d calls)", cov.calls)
	}
}

func TestHydrateOwnerRejectsACandidateOutsideTheOwnerBoundary(t *testing.T) {
	w := newWorld(t, nil)
	seedThree(w)
	// A candidate that carries a shared verdict after ranking: an
	// owner-scoped read must not hydrate it, while the unscoped read still
	// returns it.
	w.mail("shared", mail.RoleInbox, "Shared plan", "shared plan details")
	id := w.ids["shared"]
	if err := w.st.WriteVerdicts(t.Context(), "test", []store.VerdictRow{{
		RowID: id, Verdict: scope.Shared, Reason: scope.ReasonSentByOther,
	}}); err != nil {
		t.Fatal(err)
	}
	cands := []candidate{{id: id, textRank: 1}}
	terms := []store.TermMatch{{Term: "shared", Mode: store.ModeWord}, {Term: "plan", Mode: store.ModeWord}}
	ownerHits, err := w.s.hydrate(t.Context(), cands, terms, 1, 200, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(ownerHits) != 0 {
		t.Fatalf("an owner-scoped hydrate returned a shared message: %+v", ownerHits)
	}
	allHits, err := w.s.hydrate(t.Context(), cands, terms, 1, 200, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(allHits) != 1 {
		t.Fatalf("the unscoped hydrate should still return the candidate, got %d", len(allHits))
	}
}

func TestEmptyIndexAnswersWithoutEmbeddingTheQuery(t *testing.T) {
	w := newWorld(t, nil)
	seedThree(w) // text is stored, no vector has been indexed
	before := w.fake.Fake.Calls()
	res := w.search(Query{Text: "budget invoice"})
	if calls := w.fake.Fake.Calls(); calls != before {
		t.Fatalf("a search against an empty vector set embedded the query (%d calls)", calls-before)
	}
	if res.Semantic.Used {
		t.Fatalf("an empty vector set reported a semantic ranking: %+v", res.Semantic)
	}
	if res.TextMatches == 0 || len(res.Hits) == 0 {
		t.Fatalf("the full-text side did not answer: %+v", res)
	}
	if !strings.Contains(res.Notice, "no vectors") {
		t.Fatalf("notice %q", res.Notice)
	}
}

// snippetCarries reports whether a non-empty snippet contains one query term
// as the index matches it: adjacent whole tokens in word mode, a substring
// in substring/scan mode.
func snippetCarries(snippet string, terms []store.TermMatch) bool {
	for _, t := range terms {
		switch t.Mode {
		case store.ModeWord:
			if wholeToken(snippet, tokenizeFTS(mail.Fold(t.Term))...) {
				return true
			}
		case store.ModeSubstring, store.ModeScan:
			if strings.Contains(mail.Fold(snippet), mail.Fold(t.Term)) {
				return true
			}
		}
	}
	return false
}

func hitByID(t *testing.T, res Result, id string) Hit {
	t.Helper()
	for _, h := range res.Hits {
		if h.ID == id {
			return h
		}
	}
	t.Fatalf("no hit %q in %v", id, func() (out []string) {
		for _, h := range res.Hits {
			out = append(out, h.ID)
		}
		return out
	}())
	return Hit{}
}

func TestSearchSnippetAnchorsOneRuneTerms(t *testing.T) {
	w := newWorld(t, nil)
	body := strings.Repeat("filler words ", 80) + "a " + strings.Repeat("middle words ", 80) + "x " + strings.Repeat("tail words ", 80)
	w.mail("one", mail.RoleInbox, "random note", body)
	for _, term := range []string{"a", "x"} {
		res := w.search(Query{Text: term, Limit: 20})
		if len(res.Terms) != 1 || res.Terms[0].Mode != store.ModeWord {
			t.Fatalf("%q modes: %+v", term, res.Terms)
		}
		h := hitByID(t, res, "one")
		if !wholeToken(h.Snippet, term) || !strings.HasPrefix(h.Snippet, ellipsis) {
			t.Fatalf("%q is not anchored at its whole token: %q", term, h.Snippet)
		}
	}
}

func TestSearchSnippetWordBoundary(t *testing.T) {
	w := newWorld(t, nil)
	body := strings.Repeat("communication ", 40) + strings.Repeat("zzz ", 60) + "com " + strings.Repeat("tail ", 40)
	w.mail("one", mail.RoleInbox, "unrelated subject", body)
	res := w.search(Query{Text: "com", Limit: 20})
	if len(res.Terms) != 1 || res.Terms[0].Mode != store.ModeWord {
		t.Fatalf("modes: %+v", res.Terms)
	}
	h := hitByID(t, res, "one")
	if !wholeToken(h.Snippet, "com") || strings.Contains(h.Snippet, "communication") {
		t.Fatalf("com anchored a longer word: %q", h.Snippet)
	}
}

func TestSearchSnippetFieldOnlyHitsAreEmpty(t *testing.T) {
	w := newWorld(t, nil)
	subject := mailtest.Message("src", "subject-only")
	subject.Subject, subject.Body = "Quarterly report", "routine status update"
	w.put(subject)
	sender := mailtest.Message("src", "sender-only")
	sender.Subject, sender.Body = "Delivery status", "routine status update"
	sender.SenderName, sender.SenderAddress = "Postmaster", "postmaster@example.net"
	w.put(sender)
	recipient := mailtest.Message("src", "recipient-only")
	recipient.Subject, recipient.Body = "Routine note", "routine status update"
	recipient.Recipients = append(recipient.Recipients, mail.Recipient{Kind: mail.To, Address: "recipientonly@example.net"})
	w.put(recipient)

	for _, tc := range []struct{ query, id string }{
		{"quarterly", "subject-only"},
		{"postmaster", "sender-only"},
		{"recipientonly", "recipient-only"},
	} {
		res := w.search(Query{Text: tc.query, Limit: 20})
		h := hitByID(t, res, tc.id)
		if h.TextRank == 0 || h.Snippet != "" {
			t.Fatalf("%q field-only hit: text_rank=%d snippet=%q", tc.query, h.TextRank, h.Snippet)
		}
	}
	res := w.search(Query{Text: "quarterly", Limit: 20})
	if h := hitByID(t, res, "subject-only"); !strings.Contains(strings.ToLower(h.Subject), "quarterly") {
		t.Fatalf("the subject still carries the match: %+v", h.Summary)
	}
	res = w.search(Query{Text: "postmaster", Limit: 20})
	if h := hitByID(t, res, "sender-only"); !strings.Contains(strings.ToLower(h.SenderAddress), "postmaster") {
		t.Fatalf("the sender still carries the match: %+v", h.Summary)
	}
}

func TestSearchSnippetSubstringMode(t *testing.T) {
	w := newWorld(t, nil)
	w.mail("one", mail.RoleInbox, "planning notes", "communication about 会议 schedule")
	res := w.search(Query{Text: "com 会议", Limit: 20})
	modes := map[string]store.TermMode{}
	for _, term := range res.Terms {
		modes[term.Term] = term.Mode
	}
	if modes["com"] != store.ModeSubstring || (modes["会议"] != store.ModeSubstring && modes["会议"] != store.ModeScan) {
		t.Fatalf("modes: %+v", res.Terms)
	}
	h := hitByID(t, res, "one")
	if !strings.Contains(h.Snippet, "communication") || !strings.Contains(h.Snippet, "会议") {
		t.Fatalf("substring mode must anchor inside communication and keep the CJK term: %q", h.Snippet)
	}
}

func TestSearchSnippetShortScanTerm(t *testing.T) {
	w := newWorld(t, nil)
	body := strings.Repeat("maison ", 80) + "会议 " + strings.Repeat("tail ", 80)
	w.mail("one", mail.RoleInbox, "planning notes", body)
	res := w.search(Query{Text: "a 会议", Limit: 20})
	modes := map[string]store.TermMode{}
	for _, term := range res.Terms {
		modes[term.Term] = term.Mode
	}
	if modes["a"] != store.ModeScan || (modes["会议"] != store.ModeSubstring && modes["会议"] != store.ModeScan) {
		t.Fatalf("modes: %+v", res.Terms)
	}
	h := hitByID(t, res, "one")
	if !strings.Contains(h.Snippet, "会议") || !strings.HasPrefix(h.Snippet, ellipsis) {
		t.Fatalf("the one-rune scan term anchored noise: %q", h.Snippet)
	}
	res = w.search(Query{Text: "会", Limit: 20})
	if len(res.Terms) != 1 || res.Terms[0].Mode != store.ModeScan {
		t.Fatalf("single scan modes: %+v", res.Terms)
	}
	h = hitByID(t, res, "one")
	if !strings.Contains(h.Snippet, "会议") {
		t.Fatalf("a lone scan rune must anchor: %q", h.Snippet)
	}
}

// Round 30: a matra-based Indic query is scanned, and the snippet anchors
// the exact vowel sign, not the first base consonant of another sign (the
// Round 29 tokenizer splits marks in word mode).
func TestSearchSnippetIndicMatras(t *testing.T) {
	w := newWorld(t, nil)
	body := strings.Repeat("की ", 200) + strings.Repeat("tail ", 200) + "कि"
	w.mail("indic", mail.RoleInbox, "योजना", body)
	res := w.search(Query{Text: "कि", Limit: 20})
	if len(res.Terms) != 1 || res.Terms[0].Mode != store.ModeScan {
		t.Fatalf("matra query mode: %+v", res.Terms)
	}
	h := hitByID(t, res, "indic")
	if !strings.Contains(h.Snippet, "कि") || strings.Contains(h.Snippet, "की") {
		t.Fatalf("the snippet must anchor the exact matra, not the first की: %q", h.Snippet)
	}
}

func TestHydrateSnippetSemanticPreview(t *testing.T) {
	w := newWorld(t, nil)
	w.mail("one", mail.RoleInbox, "routine note", "Hello there.\n\n> quoted\n-- \nsig")
	id := w.ids["one"]
	hits, err := w.s.hydrate(t.Context(), []candidate{{id: id, textRank: 0}}, nil, 1, 60, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Snippet != "Hello there." {
		t.Fatalf("a semantic-only hit keeps the cleaned preview: %+v", hits)
	}
	hits, err = w.s.hydrate(t.Context(), []candidate{{id: id, textRank: 1}}, nil, 1, 60, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Snippet != "" {
		t.Fatalf("a full-text hit without a body match is empty: %+v", hits)
	}
}

func TestSearchSnippetNoUnrelatedBody(t *testing.T) {
	w := newWorld(t, nil)
	w.mail("com", mail.RoleInbox, "planning notes", strings.Repeat("communication ", 40)+strings.Repeat("zzz ", 40)+"com")
	w.mail("cjk", mail.RoleInbox, "planning notes", strings.Repeat("maison ", 80)+"会议 "+strings.Repeat("tail ", 80))
	w.mail("phrase", mail.RoleInbox, "bounce", "mailer "+strings.Repeat("filler ", 60)+"mailer-daemon")
	subject := mailtest.Message("src", "subject-only")
	subject.Subject, subject.Body = "Quarterly report", "routine status update"
	w.put(subject)
	sender := mailtest.Message("src", "sender-only")
	sender.Subject, sender.Body = "Delivery status", "routine status update"
	sender.SenderName, sender.SenderAddress = "Postmaster", "postmaster@example.net"
	w.put(sender)

	textHits := 0
	for _, q := range []string{"a", "x", "com", "mailer-daemon", "noreply", "phising", "a 会议", "com 会议", "quarterly", "postmaster"} {
		res := w.search(Query{Text: q, Limit: 20})
		for _, h := range res.Hits {
			if h.TextRank == 0 {
				continue
			}
			textHits++
			if h.Snippet != "" && !snippetCarries(h.Snippet, res.Terms) {
				t.Fatalf("%q: a full-text hit shows an unrelated body beginning: %q", q, h.Snippet)
			}
		}
	}
	if textHits == 0 {
		t.Fatal("the invariant ran on no full-text hit")
	}
}

// TestSearchRejectsQueryLongerThanTheConfiguredMaximum: one request must not
// become thousands of terms or one oversized embedding. The ceiling counts
// runes, not bytes, so an accented query is measured the way the owner types
// it.
func TestSearchRejectsQueryLongerThanTheConfiguredMaximum(t *testing.T) {
	w := newWorld(t, nil)
	seedThree(w)
	if err := w.cfg.Set("search.query_max_chars", 32); err != nil {
		t.Fatal(err)
	}
	_, err := w.s.Search(t.Context(), Query{Text: strings.Repeat("é", 33)})
	var tooLong *QueryTooLongError
	if !errors.As(err, &tooLong) || tooLong.Max != 32 {
		t.Fatalf("33-rune query = %v, want the 32-character ceiling", err)
	}
	if _, err := w.s.Search(t.Context(), Query{Text: strings.Repeat("é", 32)}); errors.As(err, &tooLong) {
		t.Fatalf("a query exactly at the ceiling was rejected: %v", err)
	}
}

// TestPrepareSerializesOneSpace: one search may prepare a space at a time.
// A second query waits for the gate instead of spawning another refresh
// worker against the same embedder and vector lock; a different space is
// independent.
func TestPrepareSerializesOneSpace(t *testing.T) {
	s := &Searcher{}
	ctx := t.Context()
	first, err := s.acquirePrep(ctx, "space")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.acquirePrep(ctx, "other")
	if err != nil {
		t.Fatalf("a different space was blocked: %v", err)
	}
	other()

	second := make(chan struct{})
	go func() {
		release, err := s.acquirePrep(ctx, "space")
		if err == nil {
			release()
		}
		close(second)
	}()
	select {
	case <-second:
		t.Fatal("a second search acquired the space while the first held it")
	case <-time.After(50 * time.Millisecond):
	}
	first()
	select {
	case <-second:
	case <-time.After(time.Second):
		t.Fatal("the second search did not acquire after the first released")
	}
}
