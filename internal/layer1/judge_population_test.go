package layer1

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/selective"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// seedJudgeRow writes one judge label with its prompt provenance.
func seedJudgeRow(t *testing.T, st *store.Store, id int64, value, prompt string) {
	t.Helper()
	if err := st.PutLabels(t.Context(), []store.LabelWrite{{MessageID: id, Question: "category",
		Value: value, Source: store.LabelJudge, Prompt: prompt}}); err != nil {
		t.Fatal(err)
	}
}

// A label from another prompt is stale: the same message is bought again
// under the current prompt instead of blocking the run forever.
func TestJudgePopulationRebuysAnotherPromptsLabel(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 12, nil)
	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	questions, err := judgeQuestions(plan, []string{"category"})
	if err != nil {
		t.Fatal(err)
	}
	id := w.ids[1]
	seedJudgeRow(t, w.st, id, `"work"`, "old")
	items, err := w.r.judgePopulation(t.Context(), plan, questions, 10, "new", store.LabelJudge, false, []int64{id})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].rowID != id {
		t.Fatalf("an old-prompt label blocked the message: %+v", items)
	}
}

// The population skips only a manual label or the same source's label under
// the current prompt digest.
func TestJudgePopulationSkipsManualAndCurrentPrompt(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 12, nil)
	ctx := t.Context()
	manual, current, stale := w.ids[1], w.ids[2], w.ids[4]
	if err := w.st.PutLabels(ctx, []store.LabelWrite{{MessageID: manual, Question: "category",
		Value: `"work"`, Source: store.LabelManual}}); err != nil {
		t.Fatal(err)
	}
	seedJudgeRow(t, w.st, current, `"work"`, "new")
	seedJudgeRow(t, w.st, stale, `"finance"`, "old")
	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	questions, err := judgeQuestions(plan, []string{"category"})
	if err != nil {
		t.Fatal(err)
	}
	items, err := w.r.judgePopulation(ctx, plan, questions, 10, "new", store.LabelJudge, false,
		[]int64{manual, current, stale})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].rowID != stale {
		t.Fatalf("selected %+v, want only the stale-prompt message %d", items, stale)
	}
}

// The cap still bounds a batch after the prompt filter.
func TestJudgePopulationCapStillBoundsTheBatch(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 12, nil)
	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	questions, err := judgeQuestions(plan, []string{"category"})
	if err != nil {
		t.Fatal(err)
	}
	items, err := w.r.judgePopulation(t.Context(), plan, questions, 2, "cap", store.LabelJudge, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("selected %d, want the cap 2", len(items))
	}
}

// writeJudge still supersedes a label: the one row for a message and
// question carries the new value and prompt, not a second row.
func TestWriteJudgeSupersedesTheOldPromptLabel(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 6, nil)
	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	id := w.ids[1]
	old := taxonomy.Label{Choice: "finance"}
	if _, err := w.r.writeJudge(t.Context(), plan.Profile, id, "old", store.LabelJudge,
		map[string]*taxonomy.Label{"category": &old}); err != nil {
		t.Fatal(err)
	}
	fresh := taxonomy.Label{Choice: "work"}
	if n, err := w.r.writeJudge(t.Context(), plan.Profile, id, "new", store.LabelJudge,
		map[string]*taxonomy.Label{"category": &fresh}); err != nil || n != 1 {
		t.Fatalf("stored %d labels (%v)", n, err)
	}
	rows, err := w.st.ListLabels(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, row := range rows {
		if row.MessageID != id || row.Question != "category" || row.Source != store.LabelJudge {
			continue
		}
		found++
		if row.Value != `"work"` || row.Prompt != "new" {
			t.Fatalf("the superseded row is %+v", row)
		}
	}
	if found != 1 {
		t.Fatalf("%d judge rows for message %d, want 1", found, id)
	}
}

// The audit population is the held-out split only: a message that already
// carries a reference label and has no production answer yet. A selection
// message is never audited, and a message without a reference is never
// audited.
func TestJudgeAuditSelectsOnlyReferenceLabelsInCertificationPart(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 12, nil)
	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	questions, err := judgeQuestions(plan, []string{"category"})
	if err != nil {
		t.Fatal(err)
	}
	members, err := w.r.in.Members(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	reference := map[int64]bool{}
	certify := map[int64]bool{}
	for _, m := range members {
		if selective.InCertifyPart(m.Group, plan.Config.CertifyFraction) {
			certify[m.ID] = true
		}
	}
	rows, err := w.r.in.Labels(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Source == store.LabelJudge && row.Question == "category" {
			reference[row.MessageID] = true
		}
	}
	if len(reference) == 0 {
		t.Fatal("the fixture wrote no reference labels")
	}
	items, err := w.r.judgePopulation(t.Context(), plan, questions, 100, "audit-prompt", store.LabelJudgeAnswer, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Fatal("the audit selected nothing")
	}
	for _, item := range items {
		if !reference[item.rowID] || !certify[item.rowID] {
			t.Fatalf("audit selected message %d (reference=%v certify=%v)", item.rowID, reference[item.rowID], certify[item.rowID])
		}
	}
}

// Production answers the newest mail first, so a bounded run spends its
// budget where the owner is looking.
func TestJudgeProductionAnswersNewestFirst(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 12, nil)
	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	questions, err := judgeQuestions(plan, []string{"category"})
	if err != nil {
		t.Fatal(err)
	}
	items, err := w.r.judgePopulation(t.Context(), plan, questions, 3, "prod-prompt", store.LabelJudgeAnswer, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("selected %d, want 3", len(items))
	}
	if items[0].rowID <= items[1].rowID || items[1].rowID <= items[2].rowID {
		t.Fatalf("not newest first: %d %d %d", items[0].rowID, items[1].rowID, items[2].rowID)
	}
}

// The automatic label loop passes the uncertainty queue's message ids;
// the judge must buy exactly those messages, in that order, not the
// oldest mail.
func TestJudgeMessageIDsPreserveTheQueueOrder(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 12, nil)
	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	questions, err := judgeQuestions(plan, []string{"category"})
	if err != nil {
		t.Fatal(err)
	}
	members, err := w.r.in.Members(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(members) < 6 {
		t.Fatalf("fixture has %d members", len(members))
	}
	ids := []int64{members[5].ID, members[1].ID, members[3].ID}
	items, err := w.r.judgePopulation(t.Context(), plan, questions, 10, "queue-prompt", store.LabelJudgeAnswer, false, ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != len(ids) {
		t.Fatalf("selected %d, want %d", len(items), len(ids))
	}
	for i, item := range items {
		if item.rowID != ids[i] {
			t.Fatalf("item %d is %d, want %d (queue order)", i, item.rowID, ids[i])
		}
	}
}
