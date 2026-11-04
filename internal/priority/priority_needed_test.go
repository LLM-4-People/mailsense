package priority

import (
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/route"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// newServiceAt is newService with a fixed clock, so parity across stream
// orders is not hidden by a moving recency score.
func newServiceAt(t *testing.T, rows []store.PriorityRow, clk *fakeClock) *Service {
	t.Helper()
	cfg := configtest.New(t, map[string]any{"learning.test_mode": false})
	svc, err := New(cfg, Deps{Store: &fakeStore{rows: rows}, Gate: allowGate{}, Config: cfg, Clock: clk, RouteKey: testRouteKey})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// TestNeededQuestionsCoverTheRankingRoles is the drift guard for the one
// question selection the priority read sends to the store. signals() is the
// only reader of the ranking's merged answers: the subject is derived from the
// values it already extracted, so the roles it selects must equal neededRoles
// in both directions. It also pins the skip for a role the profile does not
// keep.
func TestNeededQuestionsCoverTheRankingRoles(t *testing.T) {
	used := rankingRolesReadBySource(t)
	wanted := make(map[taxonomy.Role]bool, len(neededRoles))
	for _, role := range neededRoles {
		wanted[role] = true
	}
	if !wanted[taxonomy.RoleTopic] {
		t.Fatal("neededRoles must include the subject path's RoleTopic")
	}
	for role := range used {
		if !wanted[role] {
			t.Errorf("signals reads role %q, but neededRoles does not name it", role)
		}
	}
	for role := range wanted {
		if !used[role] {
			t.Errorf("neededRoles names role %q, but signals does not read it", role)
		}
	}

	p := defaultProfile(t)

	effort := p.QuestionName(taxonomy.RoleEffort)
	if effort == "" {
		t.Fatal("the default profile has no effort role to drop")
	}
	delete(p.Roles, taxonomy.RoleEffort)
	for _, key := range neededQuestions(p) {
		if key.Name == effort {
			t.Fatalf("neededQuestions selected dropped role question %q", effort)
		}
	}
	if got, want := len(neededQuestions(p)), len(neededRoles)-1; got != want {
		t.Fatalf("neededQuestions selected %d questions after dropping one role, want %d", got, want)
	}
}

// rankingRolesReadBySource walks signals() in this package, resolves every
// taxonomy.Role* selector through the role constants, and returns the roles
// it reads. It parses signals alone: adding subject.FromAnswers to the union
// would let a role signals stopped reading still look covered.
func rankingRolesReadBySource(t *testing.T) map[taxonomy.Role]bool {
	t.Helper()
	fset := token.NewFileSet()
	roles := roleConstantValues(t, fset)
	parsed, err := parser.ParseFile(fset, "signals.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var body *ast.BlockStmt
	for _, decl := range parsed.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if ok && fd.Name.Name == "signals" {
			body = fd.Body
			break
		}
	}
	if body == nil {
		t.Fatal("signals not found in signals.go")
	}
	used := map[taxonomy.Role]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		x, ok := sel.X.(*ast.Ident)
		if !ok || x.Name != "taxonomy" {
			return true
		}
		value, ok := roles[sel.Sel.Name]
		if !ok {
			// taxonomy.Role (the type) and any non-role selector.
			return true
		}
		used[taxonomy.Role(value)] = true
		return true
	})
	if len(used) == 0 {
		t.Fatal("no taxonomy roles found in signals")
	}
	return used
}

// roleConstantValues parses the Role constants out of roles.go, so the
// guard resolves selector names to their values without a second list.
func roleConstantValues(t *testing.T, fset *token.FileSet) map[string]string {
	t.Helper()
	parsed, err := parser.ParseFile(fset, "../triage/taxonomy/roles.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, decl := range parsed.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				value, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatal(err)
				}
				out[name.Name] = value
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("no role constants parsed from roles.go")
	}
	return out
}

func selectRouteAnswers(all map[string]route.Answer, keys []store.QuestionKey) map[string]route.Answer {
	out := make(map[string]route.Answer, len(keys))
	for _, key := range keys {
		if a, ok := all[key.Name]; ok {
			out[key.Name] = a
		}
	}
	return out
}

func selectPreviewAnswers(all map[string]engine.Answer, keys []store.QuestionKey) map[string]engine.Answer {
	out := make(map[string]engine.Answer, len(keys))
	for _, key := range keys {
		if a, ok := all[key.Name]; ok {
			out[key.Name] = a
		}
	}
	return out
}

// parityRouteAnswers is a complete, usable route map for every profile
// question: the starting point each parity case mutates.
func parityRouteAnswers(p *taxonomy.Profile) map[string]route.Answer {
	out := make(map[string]route.Answer, len(p.Questions))
	for i := range p.Questions {
		q := &p.Questions[i]
		switch q.Type {
		case engine.Noul:
			out[q.Name] = noul(q.Name, 0.9)
		case engine.Choice:
			out[q.Name] = choiceAns(q.Name, q.Options[0].Name)
		case engine.Score:
			out[q.Name] = score(q.Name, 0)
		}
	}
	return out
}

// TestPartialDecodeSignalsMatchFullDecode is the parity table for the
// decode change: the signals produced from the selected maps (what the
// store now returns) must equal the signals produced from the full maps
// (what it returned before) for every answer state the ranking handles.
// The preview flag is pinned to its honest meaning: it reports a fill of a
// question the ranking reads, never a fill of an unused question.
func TestPartialDecodeSignalsMatchFullDecode(t *testing.T) {
	p := defaultProfile(t)
	keys := neededQuestions(p)
	if len(keys) == 0 {
		t.Fatal("the profile has no ranking questions")
	}
	base := func() map[string]route.Answer { return parityRouteAnswers(p) }
	replyPreview := map[string]engine.Answer{"reply_needed": {Type: engine.Noul, Value: 0.9}}
	unusedRoute := func(route map[string]route.Answer) map[string]route.Answer {
		route["has_signature"] = notCertified(engine.Noul)
		return route
	}
	unusedPreview := map[string]engine.Answer{"has_signature": {Type: engine.Noul, Value: 0.9}}

	cases := []struct {
		name        string
		route       map[string]route.Answer
		preview     map[string]engine.Answer
		wantPreview bool
		rawFills    bool // the full unfiltered ApplyPreview replaces something
		wantReply   int
	}{
		{"complete", base(), nil, false, false, 1},
		{"missing", func() map[string]route.Answer {
			r := base()
			delete(r, "reply_needed")
			return r
		}(), nil, false, false, 0},
		{"abstained", func() map[string]route.Answer {
			r := base()
			r["reply_needed"] = notCertified(engine.Noul)
			return r
		}(), nil, false, false, 0},
		{"refused", func() map[string]route.Answer {
			r := base()
			r["reply_needed"] = route.Answer{Answer: engine.Answer{Type: engine.Noul, Refused: true}}
			return r
		}(), nil, false, false, 0},
		{"type mismatched", func() map[string]route.Answer {
			r := base()
			r["reply_needed"] = score("reply_needed", 1)
			return r
		}(), nil, false, false, 0},
		{"NaN and out of range", func() map[string]route.Answer {
			r := base()
			r["reply_needed"] = noul("reply_needed", math.NaN())
			r["urgency"] = score("urgency", 99)
			return r
		}(), nil, false, false, 0},
		{"low confidence", func() map[string]route.Answer {
			r := base()
			r["reply_needed"] = noul("reply_needed", 0.2)
			return r
		}(), nil, false, false, 0},
		{"preview fills an abstain", func() map[string]route.Answer {
			r := base()
			r["reply_needed"] = notCertified(engine.Noul)
			return r
		}(), replyPreview, true, true, 1},
		{"preview does not overwrite", func() map[string]route.Answer {
			r := base()
			r["reply_needed"] = noul("reply_needed", 0.2)
			return r
		}(), replyPreview, false, false, 0},
		{"preview only on an unused question", unusedRoute(base()), unusedPreview, false, true, 1},
		{"preview fills a used and an unused question", func() map[string]route.Answer {
			r := unusedRoute(base())
			r["reply_needed"] = notCertified(engine.Noul)
			return r
		}(), map[string]engine.Answer{
			"reply_needed":  {Type: engine.Noul, Value: 0.9},
			"has_signature": {Type: engine.Noul, Value: 0.9},
		}, true, true, 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			full, err := signalsFor(p, tc.route, tc.preview)
			if err != nil {
				t.Fatal(err)
			}
			partial, err := signalsFor(p, selectRouteAnswers(tc.route, keys), selectPreviewAnswers(tc.preview, keys))
			if err != nil {
				t.Fatal(err)
			}
			withKeys, err := signalsForKeys(p, keys, selectRouteAnswers(tc.route, keys), selectPreviewAnswers(tc.preview, keys))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(full, partial) {
				t.Fatalf("partial decode signals differ from the full decode:\n partial %+v\n full %+v", partial, full)
			}
			if !reflect.DeepEqual(partial, withKeys) {
				t.Fatalf("signalsForKeys differs from signalsFor:\n withKeys %+v\n signalsFor %+v", withKeys, partial)
			}
			if full.preview != tc.wantPreview {
				t.Fatalf("preview flag %v, want %v", full.preview, tc.wantPreview)
			}
			if full.sig.ReplyNeeded != tc.wantReply {
				t.Fatalf("reply signal %d, want %d", full.sig.ReplyNeeded, tc.wantReply)
			}
			if tc.rawFills {
				have := make(map[string]engine.Answer, len(tc.route))
				for name, a := range tc.route {
					have[name] = a.Answer
				}
				changed, err := triage.ApplyPreview(p, have, tc.preview)
				if err != nil {
					t.Fatal(err)
				}
				if !changed {
					t.Fatal("the unfiltered ApplyPreview should replace an answer in this case")
				}
			}
		})
	}
}

// TestRankingIsIndependentOfStreamOrder pins the consumer contract that
// lets the store drop ORDER BY: the heap and the topic evidence own a
// total order, so the ranked page is identical whatever order the rows
// arrive in.
func TestRankingIsIndependentOfStreamOrder(t *testing.T) {
	base := []store.PriorityRow{
		rankFixtureRow(1, receivedAgo(0), 2, "project_work"),
		rankFixtureRow(2, receivedAgo(0), 2, "support_issue"),
		rankFixtureRow(3, receivedAgo(2*time.Hour), 3, "project_work"),
		rankFixtureRow(4, receivedAgo(0), 3, "support_issue"),
		rankFixtureRow(5, receivedAgo(time.Hour), 2, "project_work"),
		rankFixtureRow(6, receivedAgo(0), 2, "project_work"),
	}
	orders := [][]int{{0, 1, 2, 3, 4, 5}, {5, 4, 3, 2, 1, 0}, {3, 0, 5, 1, 4, 2}}
	var want Page
	for i, order := range orders {
		rows := make([]store.PriorityRow, len(order))
		for j, index := range order {
			rows[j] = base[index]
		}
		svc := newServiceAt(t, rows, &fakeClock{now: testNow})
		page, err := svc.RankPage(t.Context(), FilterAll, len(base), 0)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			want = page
			continue
		}
		if !reflect.DeepEqual(page.Items, want.Items) {
			t.Fatalf("stream order %v changed the ranked items:\n got %+v\n want %+v", order, page.Items, want.Items)
		}
		if page.Total != want.Total || page.Available != want.Available || page.Preview != want.Preview ||
			!reflect.DeepEqual(page.Counts, want.Counts) {
			t.Fatalf("stream order %v changed the page: got %+v want %+v", order, page, want)
		}
	}
}

func rankFixtureRow(id int64, received string, urgency int, topic string) store.PriorityRow {
	return store.PriorityRow{
		MessageID: id, ReceivedAt: received, FolderRole: mail.RoleInbox,
		ConversationID: "conversation-" + strconv.FormatInt(id, 10),
		Route: map[string]route.Answer{
			"reply_needed": noul("reply_needed", 0.9),
			"category":     choiceAns("category", "work"),
			"topic":        choiceAns("topic", topic),
			"urgency":      score("urgency", float64(urgency)),
			"is_question":  noul("is_question", 0.9),
		},
	}
}

// TestReplySignalsAsksForTheRankingKeys pins the wiring: the service sends
// the same selection to the store that answersFor uses for the merge.
func TestReplySignalsAsksForTheRankingKeys(t *testing.T) {
	row := rankFixtureRow(1, receivedAgo(0), 2, "project_work")
	svc, fs := newService(t, []store.PriorityRow{row}, false)
	if _, _, _, err := svc.Rank(t.Context(), FilterAll, 5); err != nil {
		t.Fatal(err)
	}
	p := defaultProfile(t)
	if got, want := fs.questionKeys(), neededQuestions(p); !reflect.DeepEqual(got, want) {
		t.Fatalf("store needed keys\n got %+v\nwant %+v", got, want)
	}

}
