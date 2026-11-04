package triage

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/learning/learningtest"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// The summary is one coverage read: it takes every count from the store
// aggregate and never materializes the population or decodes an answer.
// The second message's stored answers are malformed, so the old
// MessageRefs/EachRouteAnswer path would fail the build; the aggregate read
// is the only source (and its distinct counts cannot be mistaken for the
// fixture's real ones).
func TestTriageSummaryIsOneCoverageRead(t *testing.T) {
	w := newWorld(t, nil)
	p, err := taxonomy.Load(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	var rows []store.RouteAnswer
	for i := range 2 {
		id := w.mail("a@client.com", "A", "Budget", 0, 0)
		body, err := json.Marshal(routeAnswerSet(p, i))
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			body = []byte("{")
		}
		rows = append(rows, store.RouteAnswer{MessageID: id, Revision: w.revision(t, id), Answers: string(body)})
	}
	commitRoutes(t, w, rows)
	w.useRoute(t, w.st)

	calls := 0
	var routeKey, fingerprint, decider string
	var questions []store.QuestionKey
	w.tr.coverage = func(_ context.Context, rk, fp, dec string, _ store.MessageFilter, qs []store.QuestionKey) (store.RouteCoverage, error) {
		calls++
		routeKey, fingerprint, decider, questions = rk, fp, dec, qs
		return store.RouteCoverage{Eligible: 11, Total: 7, Fresh: 3, Stale: 2, Partial: 2, Failed: 1}, nil
	}
	sum, err := w.tr.Summary(t.Context())
	if err != nil {
		t.Fatalf("the summary decoded a stored answer: %v", err)
	}
	if sum.Coverage.Eligible != 11 || sum.Coverage.Fresh != 3 || sum.Coverage.Stale != 2 ||
		sum.Coverage.Partial != 2 || sum.Coverage.Failed != 1 || sum.Coverage.Unjudged != 4 || sum.Total != 7 {
		t.Fatalf("summary %+v", sum)
	}
	if !sum.Routed || sum.Key.Fingerprint != p.Fingerprint || sum.Key.Decider == "" {
		t.Fatalf("key %+v routed %v", sum.Key, sum.Routed)
	}
	if routeKey != testRouteKey {
		t.Fatalf("the coverage read used route key %q, want %q", routeKey, testRouteKey)
	}
	if fingerprint != p.Fingerprint || decider != sum.Key.Decider {
		t.Fatalf("the coverage read used failure key %q/%q, want %q/%q", fingerprint, decider, p.Fingerprint, sum.Key.Decider)
	}
	if len(questions) != len(p.Questions) {
		t.Fatalf("the coverage read got %d questions, want %d", len(questions), len(p.Questions))
	}
	for i, q := range questions {
		if q.Name != p.Questions[i].Name || q.Type != string(p.Questions[i].Type) {
			t.Fatalf("question %d = %+v, want %s/%s", i, q, p.Questions[i].Name, p.Questions[i].Type)
		}
	}
	// The second call is served from the cache: one aggregate build, and the
	// same snapshot pointer.
	again, err := w.tr.Summary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if again != sum {
		t.Fatal("a repeated summary was rebuilt instead of served from the cache")
	}
	if calls != 1 {
		t.Fatalf("the summary built %d coverage reads, want 1", calls)
	}
}

// The summary has its own session: the profile its questions come from, the
// answer identity and the folder roles. It must never build the triage core,
// whose causal thread analysis, behaviour, evidence facts, priors and
// history it does not read. With evidence on and no engine wired, the old
// core build fails with its documented error; neither the summary nor the
// page builds that core, so both answer.
func TestTriageSummaryDoesNotBuildTheCore(t *testing.T) {
	w := newWorld(t, nil)
	w.mail("a@client.com", "A", "Budget", 0, 0)
	w.useRoute(t, w.st)
	if err := w.cfg.Set("triage.facts.evidence", true); err != nil {
		t.Fatal(err)
	}
	reads := 0
	w.tr.coverage = func(_ context.Context, _, _, _ string, _ store.MessageFilter, _ []store.QuestionKey) (store.RouteCoverage, error) {
		reads++
		return store.RouteCoverage{Eligible: 1, Total: 1, Fresh: 1}, nil
	}
	sum, err := w.tr.Summary(t.Context())
	if err != nil {
		t.Fatalf("the summary built the core: %v", err)
	}
	if reads != 1 || sum.Coverage.Eligible != 1 || sum.Coverage.Fresh != 1 || sum.Total != 1 {
		t.Fatalf("summary %+v after %d coverage reads", sum, reads)
	}
	page, err := w.tr.Page(t.Context(), PageQuery{Limit: 1})
	if err != nil {
		t.Fatalf("the page built the core: %v", err)
	}
	if !page.Routed || page.Total != 0 || len(page.Results) != 0 {
		t.Fatalf("page %+v", page)
	}
}

// A failure is part of the summary coverage even though the route supplies
// the displayed answers: a later failed attempt must not disappear from the
// header. Only the summary's own triage key counts; another decider or
// fingerprint, and a failure for a message outside the population, do not.
func TestTriageSummaryCountsFailures(t *testing.T) {
	w := newWorld(t, nil)
	p, err := taxonomy.Load(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	id := w.mail("a@client.com", "A", "Budget", 0, 0)
	body, err := json.Marshal(routeAnswerSet(p, 0))
	if err != nil {
		t.Fatal(err)
	}
	commitRoutes(t, w, []store.RouteAnswer{{MessageID: id, Revision: w.revision(t, id), Answers: string(body)}})
	w.useRoute(t, w.st)

	// A message outside the picked store is never a triage member.
	hidden := w.message("hidden", mail.RoleInbox, "Inbox", "b@client.com", "H", "Hidden", base, me)
	hidden.StoreID = "store-2"
	w.write(hidden)
	hiddenID := w.rowOf("hidden")

	sum, err := w.tr.Summary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if sum.Coverage.Failed != 0 {
		t.Fatalf("no failure is stored yet: %+v", sum.Coverage)
	}
	key := sum.Key
	if err := w.st.SaveFailure(t.Context(), key, store.Failure{MessageID: id, Error: "provider down"}); err != nil {
		t.Fatal(err)
	}
	otherDecider := key
	otherDecider.Decider = "other/decider"
	if err := w.st.SaveFailure(t.Context(), otherDecider, store.Failure{MessageID: id, Error: "other decider"}); err != nil {
		t.Fatal(err)
	}
	otherFingerprint := key
	otherFingerprint.Fingerprint = "other-profile"
	if err := w.st.SaveFailure(t.Context(), otherFingerprint, store.Failure{MessageID: id, Error: "other profile"}); err != nil {
		t.Fatal(err)
	}
	if err := w.st.SaveFailure(t.Context(), key, store.Failure{MessageID: hiddenID, Error: "not a member"}); err != nil {
		t.Fatal(err)
	}

	// A failure write does not move the mail or route generation, so the
	// summary cache must be rebuilt by a fresh triager (the production read
	// that sees a new failure is the next cache build).
	tr, err := New(w.cfg, w.st, w.bus, w.src, learningtest.Open{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tr.UseRoute(w.st, w.routeKeyer(t))
	got, err := tr.Summary(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got.Coverage.Failed != 1 || got.Coverage.Eligible != 1 || got.Coverage.Fresh != 1 ||
		got.Coverage.Stale != 0 || got.Coverage.Partial != 0 || got.Coverage.Unjudged != 0 || got.Total != 1 {
		t.Fatalf("coverage with one failure (and three ignored rows): %+v total %d", got.Coverage, got.Total)
	}
}

// The summary build must not reintroduce the materializing read: a runtime
// spy cannot see a discarded scan, so the build's own syntax is the guard.
// MessageRefs materialized every eligible row; EachRouteAnswer plus
// DecodeStrict decoded every stored answer in Go. Both were the whole cost
// of the summary this aggregate replaces. Nor may it touch the route's core:
// core/prepare/prepareRoute load the causal analysis, behaviour, evidence,
// priors and history the summary never reads.
func TestSummaryBuildHasNoMaterializingRead(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "page.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var body *ast.BlockStmt
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == "buildSummary" {
			body = fn.Body
		}
	}
	if body == nil {
		t.Fatal("buildSummary not found in page.go")
	}
	forbidden := map[string]bool{
		"MessageRefs":     true,
		"EachRouteAnswer": true,
		"DecodeStrict":    true,
		"jsonx":           true,
		"route":           true,
		"core":            true,
		"prepare":         true,
		"prepareRoute":    true,
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if forbidden[x.Sel.Name] {
				t.Errorf("buildSummary calls %s, which the aggregate replaces", x.Sel.Name)
			}
		case *ast.Ident:
			if forbidden[x.Name] {
				t.Errorf("buildSummary references %s, which the aggregate replaces", x.Name)
			}
		}
		return true
	})
}
