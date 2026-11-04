package triage

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/route"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// pageProfile drops the category and topic questions so the first page value
// is the reply role: the row then carries the fields only a blend/decided
// value has (Prior on the full build, Decided on both). It also raises the
// reply escalation threshold above the fixture's confident 0.9 answer, so
// one case escalates and the rest do not.
func pageProfile(t testing.TB, w *world) {
	t.Helper()
	path := filepath.Join(w.cfg.Paths().DataDir, "page-profile.json")
	body := []byte(`{
		"questions": {"category": null, "topic": null},
		"roles": {"category": null, "topic": null},
		"escalation": {"reply": {"min_confidence": 0.85}}
	}`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := w.cfg.Set(taxonomy.ProfileFileSetting, path); err != nil {
		t.Fatal(err)
	}
}

// fullCorePage is the pre-page-session build kept as the equivalence
// reference: the full causal core (prepareRoute), the full result (priors,
// subject and coverage) and pageRowOf. It is test-only; buildPage no longer
// takes this path.
func fullCorePage(t testing.TB, tr *Triager, routeKey string, limit, offset int) *Page {
	t.Helper()
	s, err := tr.prepareRoute(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	out := &Page{Key: s.key, Profile: s.profile, Results: map[int64]PageRow{}, IDs: []int64{}, Routed: true,
		Preview: tr.previewing(t.Context()), Limit: limit, Offset: offset}
	window, total, err := tr.st.RoutePage(t.Context(), routeKey, store.MessageFilter{Roles: s.roles, OwnerOnly: true}, limit, offset)
	if err != nil {
		t.Fatal(err)
	}
	out.Total = total
	if len(window) == 0 {
		return out
	}
	ids := make([]int64, len(window))
	for i, ref := range window {
		ids[i] = ref.MessageID
	}
	records, err := tr.route.LoadRouteRecords(t.Context(), routeKey, ids)
	if err != nil {
		t.Fatal(err)
	}
	previews, err := tr.loadPreviews(t.Context(), routeKey, ids)
	if err != nil {
		t.Fatal(err)
	}
	var coverage Coverage
	err = s.eachEligible(t.Context(), tr.st, nil, ids, func(r *store.MessageRow) error {
		rec, ok := records[r.RowID]
		a := messageAnswers{}
		if ok {
			a.Answers, a.Missing = routeAnswers(s.profile, rec.Answers)
			a.At, a.Stale = rec.DecidedAt, rec.Revision != r.Revision
		}
		if prec, ok := previews[r.RowID]; ok {
			changed, err := previewAnswers(s.profile, a.Answers, prec.Answers)
			if err != nil {
				return err
			}
			a.Preview = changed
		}
		res, shown := s.result(r, a, &coverage)
		if shown {
			out.Results[r.RowID] = pageRowOf(res, s.profile)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The reference page carries the displayed order too: the equivalence
	// test compares the wire JSON, and a hidden window row has no id there.
	out.IDs = make([]int64, 0, len(out.Results))
	for _, ref := range window {
		if _, ok := out.Results[ref.MessageID]; ok {
			out.IDs = append(out.IDs, ref.MessageID)
		}
	}
	return out
}

// rowWithoutPrior is one PageRow with the diagnostic prior dropped: the
// wire never carries it, and the page has no priors at all.
func rowWithoutPrior(row PageRow) PageRow {
	row.FirstValue.Prior = nil
	return row
}

// The row counts usable answers, present abstentions/refusals and absent
// questions apart, and the first value is the first answered question: an
// earlier abstention must not take the slot.
func TestPageRowCountsAnswersAbstentionsAndMissing(t *testing.T) {
	profile := &taxonomy.Profile{Questions: []engine.Question{
		{Name: "reply_needed", Type: engine.Noul},
		{Name: "urgency", Type: engine.Score},
		{Name: "category", Type: engine.Choice},
		{Name: "topic", Type: engine.Choice},
		{Name: "owner", Type: engine.Noul},
		{Name: "deadline", Type: engine.Noul},
	}}
	second := taxonomy.Value{Type: engine.Score, Number: 3, Decided: "3"}
	res := Result{MessageID: 7, Values: map[string]taxonomy.Value{
		"reply_needed": {Type: engine.Noul, Abstain: route.AbstainNotCertified},
		"urgency":      second,
		"category":     {Type: engine.Choice, Refused: true},
		"topic":        {Type: engine.Choice, Abstain: engine.AbstainBelowTarget},
		"owner":        {Type: engine.Noul, Number: 0, Decided: "no"},
		// Outside the profile: the route/profile owns the question set, so
		// this value is ignored by every count and by the first-value slot.
		"outside": {Type: engine.Noul, Number: 0.5, Decided: "yes"},
	}, Missing: []string{"deadline"}}

	row := pageRowOf(res, profile)
	if row.Answered != 2 || row.Abstained != 3 || row.Missing != 1 {
		t.Fatalf("row counts: answered %d, abstained %d, missing %d", row.Answered, row.Abstained, row.Missing)
	}
	if row.FirstQuestion != "urgency" || !reflect.DeepEqual(row.FirstValue, second) {
		t.Fatalf("first value = %q %+v, want urgency %+v", row.FirstQuestion, row.FirstValue, second)
	}
	if row.Answered+row.Abstained+row.Missing != len(profile.Questions) {
		t.Fatalf("the three counts do not cover the profile: %+v", row)
	}
}

// Abstained is part of the wire contract even at zero: the UI reads the
// count directly and must not fall back to the answered/missing pair.
func TestPageRowJSONCarriesAbstainedCount(t *testing.T) {
	body, err := json.Marshal(PageRow{MessageID: 7, Answered: 2, Abstained: 3, Missing: 1})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["abstained"] != float64(3) {
		t.Fatalf("abstained count on the wire: %s", body)
	}

	body, err = json.Marshal(PageRow{MessageID: 8})
	if err != nil {
		t.Fatal(err)
	}
	decoded = nil
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if got, ok := decoded["abstained"]; !ok || got != float64(0) {
		t.Fatalf("an empty count is not serialized: %s", body)
	}
}

// The page's lightweight build must equal the full-core build field for
// field, including the wire JSON. The fixture covers the row shapes the list
// renders: fresh, missing (the first question absent), stale, preview,
// escalating, fully abstained and refused.
func TestPageRowsMatchTheFullCoreBuild(t *testing.T) {
	w := newWorld(t, nil)
	pageProfile(t, w)
	// A replied-to seed message gives the later fixture mail a causal prior;
	// the full build carries it in the first value, which the page row drops.
	w.mail("a@client.com", "seed", "Seed", -2*time.Hour, time.Hour)
	p, err := taxonomy.Load(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	first := p.Questions[0].Name
	if first != "reply_needed" {
		t.Fatalf("the fixture expects reply_needed first, got %q", first)
	}

	const (
		fresh = iota
		missing
		stale
		preview
		escalate
		abstained
		refused
	)
	ids := make([]int64, 7)
	for i := range ids {
		ids[i] = w.mail("a@client.com", string(rune('A'+i)), "Budget", time.Duration(i)*time.Hour, 0)
	}
	answers := func(i int) map[string]route.Answer {
		a := routeAnswerSet(p, i)
		// The merged profile escalates a score without a confidence; give the
		// fixture's score answers one so only the reply case can be uncertain.
		for j := range p.Questions {
			if q := &p.Questions[j]; q.Type == engine.Score {
				rec := a[q.Name]
				rec.Answer.Confidence = confidence(0.9)
				a[q.Name] = rec
			}
		}
		switch i {
		case escalate:
			rec := a[first]
			rec.Answer.Value = 0.7 // noul confidence 0.7 < 0.85
			a[first] = rec
		case missing:
			delete(a, first)
		case preview:
			a[first] = route.Answer{Answer: engine.Answer{Type: engine.Noul, Abstain: route.AbstainNotCertified}}
		case abstained:
			for name, rec := range a {
				a[name] = route.Answer{Answer: engine.Answer{Type: rec.Type, Abstain: route.AbstainNotCertified}}
			}
		case refused:
			rec := a[first]
			rec.Answer.Refused = true
			a[first] = rec
		}
		return a
	}
	var committed []store.RouteAnswer
	for i, id := range ids {
		body, err := json.Marshal(answers(i))
		if err != nil {
			t.Fatal(err)
		}
		committed = append(committed, store.RouteAnswer{MessageID: id, Revision: w.revision(t, id), Answers: string(body)})
	}
	commitRoutes(t, w, committed)

	records := map[int64]store.RouteRecord{}
	for i, id := range ids {
		rev := w.revision(t, id)
		if i == stale {
			rev-- // the row was computed for an older state of the message
		}
		records[id] = store.RouteRecord{MessageID: id, Revision: rev, DecidedAt: "2026-10-25T10:00:00Z", Answers: answers(i)}
	}
	fr := &fakeRoute{records: records, previews: map[int64]store.PreviewRecord{
		ids[preview]: {MessageID: ids[preview], Revision: w.revision(t, ids[preview]), DecidedAt: "2026-10-25T10:00:00Z",
			Answers: map[string]engine.Answer{first: {Type: engine.Noul, Value: 0.9}}},
	}}
	w.useRoute(t, fr)
	w.tr.gate = previewingGate{}

	got, err := w.tr.buildPage(t.Context(), testRouteKey, TriagePageDefault, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := fullCorePage(t, w.tr, testRouteKey, TriagePageDefault, 0)

	if got.Total != want.Total || got.Limit != want.Limit || got.Offset != want.Offset ||
		got.Routed != want.Routed || got.Preview != want.Preview || got.Key != want.Key {
		t.Fatalf("page envelope %+v, want %+v", got, want)
	}
	if len(got.Results) != len(want.Results) {
		t.Fatalf("page has %d rows, the full build has %d", len(got.Results), len(want.Results))
	}
	for id, wantRow := range want.Results {
		gotRow, ok := got.Results[id]
		if !ok {
			t.Fatalf("page lacks message %d", id)
		}
		if !reflect.DeepEqual(rowWithoutPrior(gotRow), rowWithoutPrior(wantRow)) {
			t.Fatalf("message %d: page row %+v, full build %+v", id, gotRow, wantRow)
		}
	}

	// The fixture must actually exercise each shape, so equal rows cannot
	// hide a field the page never fills.
	if got.Results[ids[fresh]].Preview || got.Results[ids[fresh]].Stale || got.Results[ids[fresh]].Escalate {
		t.Fatalf("fresh row %+v", got.Results[ids[fresh]])
	}
	if got.Results[ids[fresh]].FirstValue.Decided == "" {
		t.Fatalf("fresh row carries no certified class: %+v", got.Results[ids[fresh]])
	}
	if want.Results[ids[fresh]].FirstValue.Prior == nil {
		t.Fatalf("the full build carries no prior to ignore: %+v", want.Results[ids[fresh]])
	}
	if got.Results[ids[fresh]].FirstValue.Prior != nil {
		t.Fatalf("the page carries a prior: %+v", got.Results[ids[fresh]])
	}
	if got.Results[ids[missing]].Missing == 0 || got.Results[ids[missing]].FirstQuestion == first {
		t.Fatalf("missing row %+v", got.Results[ids[missing]])
	}
	if !got.Results[ids[stale]].Stale {
		t.Fatalf("stale row %+v", got.Results[ids[stale]])
	}
	if !got.Results[ids[preview]].Preview || got.Results[ids[preview]].FirstValue.Abstain != "" ||
		got.Results[ids[preview]].FirstQuestion != first {
		t.Fatalf("preview row %+v", got.Results[ids[preview]])
	}
	if !got.Results[ids[escalate]].Escalate {
		t.Fatalf("escalating row %+v", got.Results[ids[escalate]])
	}
	if row := got.Results[ids[abstained]]; row.Answered != 0 || row.Abstained != len(p.Questions) ||
		row.FirstQuestion != "" || row.FirstValue.Type != "" {
		t.Fatalf("fully abstained row %+v", row)
	}
	if row := got.Results[ids[refused]]; row.Answered != len(p.Questions)-1 || row.Abstained != 1 ||
		row.FirstQuestion != p.Questions[1].Name {
		t.Fatalf("refused row %+v", row)
	}

	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("page JSON differs:\n got: %s\nwant: %s", gotJSON, wantJSON)
	}
}

// countingEvidence counts the causal core's probes: Version is read at the
// top of core() (twice per call, through stamp), Facts once inside
// buildCore after threads.Load. A page build that never enters the core
// leaves both at zero; the old full-core path increments them.
type countingEvidence struct {
	versions int
	facts    int
}

func (c *countingEvidence) Version() string { c.versions++; return "counting" }

func (c *countingEvidence) Facts(context.Context) (map[int64]string, error) {
	c.facts++
	return map[int64]string{}, nil
}

// The page build must not load the causal core: no threads.Load, no
// feedback.Load, no Priors, no composer. The counters prove the core was
// never entered (0), and the old path on the same fixture proves the seam
// sees a build (1 facts load) when it happens.
func TestPageBuildDoesNotLoadTheCausalCore(t *testing.T) {
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

	ev := &countingEvidence{}
	w.tr.UseEvidence(ev)
	if err := w.cfg.Set("triage.facts.evidence", true); err != nil {
		t.Fatal(err)
	}
	w.useRoute(t, w.st)

	page, err := w.tr.buildPage(t.Context(), testRouteKey, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Results) != 1 {
		t.Fatalf("page %+v", page)
	}
	if ev.versions != 0 || ev.facts != 0 {
		t.Fatalf("the page entered the causal core: %d version probes, %d fact loads", ev.versions, ev.facts)
	}
	if w.tr.cache != nil {
		t.Fatal("the page build populated the core cache")
	}

	// The same fixture through the old page path (prepareRoute + result):
	// the counter must see it, so the zero above is a real absence and not a
	// seam that can never fire.
	_ = fullCorePage(t, w.tr, testRouteKey, 1, 0)
	if ev.facts != 1 {
		t.Fatalf("the full-core reference made %d fact loads, want 1", ev.facts)
	}
	if ev.versions == 0 || w.tr.cache == nil {
		t.Fatalf("the full-core reference did not build the core: %d version probes, cache %v", ev.versions, w.tr.cache)
	}
}
