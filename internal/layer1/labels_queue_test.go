package layer1

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/selective"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// The queue offers only unlabelled members, is deterministic, respects its
// bounds, and carries no message content.
func TestLabelQueueIsDeterministicAndContentFree(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 60, nil)
	permissiveRequirements(t, w)
	w.run()
	query := LabelQuery{Question: "category", Limit: 5}
	first, err := w.r.LabelQueue(t.Context(), query)
	if err != nil || len(first.Tasks) != 5 || first.Question != "category" || first.Strategy != LabelStrategyUncertainty {
		t.Fatalf("queue %+v %v", first, err)
	}
	labelled := map[int64]bool{}
	rows, err := w.st.ListLabels(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Question == "category" {
			labelled[row.MessageID] = true
		}
	}
	member := map[int64]bool{}
	for _, id := range w.ids {
		member[id] = true
	}
	for _, task := range first.Tasks {
		if !member[task.MessageID] || labelled[task.MessageID] || task.Question != "category" || task.Reason != LabelReasonUncertain {
			t.Fatalf("task %+v (member %v, labelled %v)", task, member[task.MessageID], labelled[task.MessageID])
		}
	}
	again, err := w.r.LabelQueue(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(again)
	if string(a) != string(b) {
		t.Fatalf("the queue is not deterministic:\n%s\n%s", a, b)
	}
	if auto, err := w.r.LabelQueue(t.Context(), LabelQuery{Question: "category", Limit: 5, Strategy: LabelStrategyAuto}); err != nil || len(auto.Tasks) != 5 || auto.Strategy != LabelStrategyUncertainty {
		t.Fatalf("auto strategy %+v %v", auto, err)
	}
	for _, secret := range []string{"subject ", "billing@vendor.example", "colleague@corp.example", "words", "work", "finance"} {
		if strings.Contains(string(a), secret) {
			t.Fatalf("the queue leaks %q: %s", secret, a)
		}
	}
	all, err := w.r.LabelQueue(t.Context(), LabelQuery{Question: "category", Limit: LabelQueueMax})
	if err != nil || len(all.Tasks) != 40 {
		t.Fatalf("fewer than the limit returns what exists: %d %v", len(all.Tasks), err)
	}
	def, err := w.r.LabelQueue(t.Context(), LabelQuery{Question: "category"})
	if err != nil || len(def.Tasks) != LabelQueueDefault {
		t.Fatalf("the default limit: %d %v", len(def.Tasks), err)
	}
	for _, limit := range []int{-1, LabelQueueMax + 1} {
		if _, err := w.r.LabelQueue(t.Context(), LabelQuery{Question: "category", Limit: limit}); err == nil {
			t.Errorf("limit %d was accepted", limit)
		}
	}
	if _, err := w.r.LabelQueue(t.Context(), LabelQuery{Question: "not_a_question"}); err == nil {
		t.Error("an unknown question was accepted")
	}
	if _, err := w.r.LabelQueue(t.Context(), LabelQuery{Question: "category", Strategy: "random"}); err == nil {
		t.Error("an unknown strategy was accepted")
	}
}

// Uncertainty ranks messages by the route's confidence (closest to its
// decision boundary first); missing orders by id. No queue reads a label
// value.
func TestLabelQueueOrder(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 60, nil)
	if err := writeRequirements(w.cfg, `{"defaults": {"min_effective_train": 10, "min_eval_labels": 4, "target_accuracy": 0.7, "min_coverage": 0.3, "z": 1.2, "family_wise": false}}`); err != nil {
		t.Fatal(err)
	}
	w.run()
	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	q, ok := plan.Profile.Question("category")
	if !ok {
		t.Fatal("category")
	}
	answers, err := w.st.LoadRouteAnswers(t.Context(), w.routeKey(t), w.ids)
	if err != nil {
		t.Fatal(err)
	}
	cutoff := plan.Profile.Cutoff("category")
	queue, err := w.r.LabelQueue(t.Context(), LabelQuery{Question: "category", Limit: LabelQueueMax})
	if err != nil {
		t.Fatal(err)
	}
	given := 0
	prev := 0.0
	for _, task := range queue.Tasks {
		a, ok := answers[task.MessageID][q.Name]
		confidence, known := 0.0, false
		if ok {
			confidence, known = selective.Confidence(a.Answer, cutoff)
		}
		if !known {
			t.Fatalf("an abstained message %d ranks in the queue", task.MessageID)
		}
		given++
		if given > 1 && confidence < prev {
			t.Fatalf("confidences are not ascending: %v after %v at message %d", confidence, prev, task.MessageID)
		}
		prev = confidence
	}
	if given == 0 {
		t.Fatal("the world answered no message")
	}
	missing, err := w.r.LabelQueue(t.Context(), LabelQuery{Question: "category", Limit: LabelQueueMax, Strategy: LabelStrategyMissing})
	if err != nil {
		t.Fatal(err)
	}
	for i, task := range missing.Tasks {
		if task.Reason != LabelReasonMissing || (i > 0 && task.MessageID <= missing.Tasks[i-1].MessageID) {
			t.Fatalf("missing order at %d: %+v", i, task)
		}
	}
}

// A route answer that abstained (or does not exist) is more uncertain than
// any given answer: with the default target every answer abstains, so the
// queue is still deterministic and ordered by id.
func TestLabelQueueRanksAbstainedFirst(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 60, nil)
	w.run()
	queue, err := w.r.LabelQueue(t.Context(), LabelQuery{Question: "category", Limit: LabelQueueMax})
	if err != nil {
		t.Fatal(err)
	}
	if len(queue.Tasks) == 0 {
		t.Fatal("no task")
	}
	answers, err := w.st.LoadRouteAnswers(t.Context(), w.routeKey(t), w.ids)
	if err != nil {
		t.Fatal(err)
	}
	for i, task := range queue.Tasks {
		if task.Reason != LabelReasonUncertain || (i > 0 && task.MessageID <= queue.Tasks[i-1].MessageID) {
			t.Fatalf("abstained order at %d: %+v", i, task)
		}
		if a, ok := answers[task.MessageID]["category"]; ok && a.Abstain == "" {
			t.Fatalf("message %d is given and ranks first: %+v", task.MessageID, a)
		}
	}
}

// Review offers the visible members with a judge label for the question
// and no manual label, with the judge value decoded for the answer
// controls. A manual label answers the question and removes the message;
// the normal queue keeps its own population and leaks no value.
func TestLabelQueueReviewOffersJudgeSuggestions(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 60, nil)
	permissiveRequirements(t, w)
	w.run()
	query := LabelQuery{Question: "category", Limit: LabelQueueMax, Review: true}
	review, err := w.r.LabelQueue(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	if len(review.Tasks) != 20 {
		t.Fatalf("review offers %d tasks, want the 20 judge-labelled messages", len(review.Tasks))
	}
	if review.Question != "category" || review.Strategy != LabelStrategyUncertainty {
		t.Fatalf("review queue question %q strategy %q", review.Question, review.Strategy)
	}
	seen := make(map[int64]bool, len(review.Tasks))
	for i, task := range review.Tasks {
		if task.Question != "category" || task.Reason != LabelReasonReview || task.SuggestionSource != store.LabelJudge {
			t.Fatalf("review task %d: question %q, reason %q, source %q", i, task.Question, task.Reason, task.SuggestionSource)
		}
		suggestion, ok := task.Suggestion.(string)
		if !ok {
			t.Fatalf("review task %d carries %T, want an option name", i, task.Suggestion)
		}
		index := w.index(task.MessageID)
		if index < 0 || index%3 != 0 {
			t.Fatalf("review task %d is not a judge-labelled member (index %d)", i, index)
		}
		if want := []string{"finance", "work"}[index%2]; suggestion != want {
			t.Fatalf("review task %d decodes %q, want %q", i, suggestion, want)
		}
		if seen[task.MessageID] {
			t.Fatalf("review task %d repeats a message", i)
		}
		seen[task.MessageID] = true
	}
	again, err := w.r.LabelQueue(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(review)
	b, _ := json.Marshal(again)
	if string(a) != string(b) {
		t.Fatalf("the review queue is not deterministic:\n%s\n%s", a, b)
	}
	ordered, err := w.r.LabelQueue(t.Context(), LabelQuery{Question: "category", Limit: LabelQueueMax, Strategy: LabelStrategyMissing, Review: true})
	if err != nil {
		t.Fatal(err)
	}
	for i, task := range ordered.Tasks {
		if i > 0 && task.MessageID <= ordered.Tasks[i-1].MessageID {
			t.Fatalf("review missing order at %d", i)
		}
	}
	paged, err := w.r.LabelQueue(t.Context(), LabelQuery{Question: "category", Limit: 5, Review: true})
	if err != nil || len(paged.Tasks) != 5 {
		t.Fatalf("review limit: %d tasks, %v", len(paged.Tasks), err)
	}

	// A valid manual label answers the question, so the message leaves
	// review; a manual answer without a judge label was never offered.
	if err := w.st.PutLabel(t.Context(), w.ids[1], "category", `"work"`, store.LabelManual); err != nil {
		t.Fatal(err)
	}
	if err := w.st.PutLabel(t.Context(), w.ids[0], "category", `"finance"`, store.LabelManual); err != nil {
		t.Fatal(err)
	}
	after, err := w.r.LabelQueue(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Tasks) != 19 {
		t.Fatalf("review offers %d tasks after one manual answer, want 19", len(after.Tasks))
	}
	for i, task := range after.Tasks {
		if task.MessageID == w.ids[0] {
			t.Fatalf("review task %d is still offered after a manual label", i)
		}
		if task.MessageID == w.ids[1] {
			t.Fatalf("review task %d is a manual-only message", i)
		}
	}

	// The normal queue keeps its own (unlabelled) population and carries
	// neither a suggestion nor a judge value.
	normal, err := w.r.LabelQueue(t.Context(), LabelQuery{Question: "category", Limit: LabelQueueMax})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(normal)
	for _, forbidden := range []string{"suggestion", `"finance"`, `"work"`, "judge"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("the normal queue leaks %q: %s", forbidden, raw)
		}
	}
	for i, task := range normal.Tasks {
		if task.Suggestion != nil || task.SuggestionSource != "" || task.Reason != LabelReasonUncertain {
			t.Fatalf("normal task %d carries review fields", i)
		}
	}
}

// Review decodes every question type into the primitive the answer
// controls apply: the option name, the yes/no side (false included), and the
// level index rounded half-up.
func TestLabelQueueReviewDecodesSuggestionTypes(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 6, nil)
	for _, label := range []struct {
		id     int64
		value  string
		source string
	}{
		{w.ids[0], "true", store.LabelJudge},
		{w.ids[1], "false", store.LabelJudge},
	} {
		if err := w.st.PutLabel(t.Context(), label.id, "reply_needed", label.value, label.source); err != nil {
			t.Fatal(err)
		}
	}
	queue, err := w.r.LabelQueue(t.Context(), LabelQuery{Question: "reply_needed", Limit: LabelQueueMax, Review: true})
	if err != nil {
		t.Fatal(err)
	}
	want := map[int64]bool{w.ids[0]: true, w.ids[1]: false}
	if len(queue.Tasks) != len(want) {
		t.Fatalf("review offers %d yes/no tasks, want %d", len(queue.Tasks), len(want))
	}
	for i, task := range queue.Tasks {
		value, ok := task.Suggestion.(bool)
		if !ok {
			t.Fatalf("review task %d carries %T, want a bool", i, task.Suggestion)
		}
		expected, known := want[task.MessageID]
		if !known || value != expected {
			t.Fatalf("review task %d decodes %v, want %v", i, value, expected)
		}
		if task.SuggestionSource != store.LabelJudge || task.Reason != LabelReasonReview {
			t.Fatalf("review task %d: source %q reason %q", i, task.SuggestionSource, task.Reason)
		}
	}
	if err := w.st.PutLabel(t.Context(), w.ids[0], "urgency", "2.5", store.LabelJudge); err != nil {
		t.Fatal(err)
	}
	score, err := w.r.LabelQueue(t.Context(), LabelQuery{Question: "urgency", Limit: LabelQueueMax, Review: true})
	if err != nil || len(score.Tasks) != 1 {
		t.Fatalf("review score: %d tasks, %v", len(score.Tasks), err)
	}
	if value, ok := score.Tasks[0].Suggestion.(float64); !ok || value != 3 {
		t.Fatalf("a 2.5 level decodes as %T %v, want the half-up index 3", score.Tasks[0].Suggestion, score.Tasks[0].Suggestion)
	}
}

// An unreadable judge value is offered as a task without a suggestion (the
// badge counts it); a manual label answers the question; and unpicking the
// store hides both populations.
func TestLabelQueueReviewOffersUnreadableJudgeAndRespectsVisibility(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 60, nil)
	if err := w.st.PutLabel(t.Context(), w.ids[1], "category", `"not_an_option"`, store.LabelJudge); err != nil {
		t.Fatal(err)
	}
	if err := w.st.PutLabel(t.Context(), w.ids[2], "category", `"work"`, store.LabelManual); err != nil {
		t.Fatal(err)
	}
	review, err := w.r.LabelQueue(t.Context(), LabelQuery{Question: "category", Limit: LabelQueueMax, Review: true})
	if err != nil {
		t.Fatal(err)
	}
	// The fixture's 20 readable judge labels plus this unreadable one: the
	// unreadable task counts and is offered, with no suggestion.
	if len(review.Tasks) != 21 || review.Total != 21 {
		t.Fatalf("review offers %d tasks (total %d), want the 21 judge-labelled messages", len(review.Tasks), review.Total)
	}
	unreadable := false
	for i, task := range review.Tasks {
		if task.MessageID == w.ids[1] {
			unreadable = true
			if task.Suggestion != nil || task.SuggestionSource != "" {
				t.Fatalf("unreadable review task %d carries %T %q", i, task.Suggestion, task.SuggestionSource)
			}
		}
		if task.MessageID == w.ids[2] {
			t.Fatalf("review task %d is a manual-only message", i)
		}
	}
	if !unreadable {
		t.Fatal("the unreadable judge label was not offered")
	}
	normal, err := w.r.LabelQueue(t.Context(), LabelQuery{Question: "category", Limit: LabelQueueMax})
	if err != nil {
		t.Fatal(err)
	}
	offered := make(map[int64]bool, len(normal.Tasks))
	for _, task := range normal.Tasks {
		offered[task.MessageID] = true
	}
	if !offered[w.ids[1]] {
		t.Fatal("an unreadable judge label removed a member from the normal queue")
	}
	if offered[w.ids[2]] {
		t.Fatal("the normal queue offers a manually labelled message")
	}
	if err := w.cfg.Set(source.SettingSelected, []any{}); err != nil {
		t.Fatal(err)
	}
	hidden, err := w.r.LabelQueue(t.Context(), LabelQuery{Question: "category", Limit: LabelQueueMax, Review: true})
	if err != nil || len(hidden.Tasks) != 0 {
		t.Fatalf("an unpicked store still offers %d review tasks, %v", len(hidden.Tasks), err)
	}
	if err := w.cfg.Set(source.SettingSelected, []any{testKey}); err != nil {
		t.Fatal(err)
	}
	back, err := w.r.LabelQueue(t.Context(), LabelQuery{Question: "category", Limit: LabelQueueMax, Review: true})
	if err != nil || len(back.Tasks) != 21 {
		t.Fatalf("re-picking the store offers %d review tasks, %v", len(back.Tasks), err)
	}
}

// The review badge and the review queue share one presence-based population:
// a judge label with no manual label, decodable or not. The four fixture
// messages cover a readable judge, an unreadable judge, a readable judge with
// an unreadable manual answer, and no judge at all.
func TestReviewQueueMatchesCountsOnUndecodableValues(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 4, nil)
	ctx := t.Context()
	// ids[0] keeps the fixture's readable judge value.
	if err := w.st.PutLabel(ctx, w.ids[1], "category", `"not_an_option"`, store.LabelJudge); err != nil {
		t.Fatal(err)
	}
	if err := w.st.PutLabel(ctx, w.ids[2], "category", `"work"`, store.LabelJudge); err != nil {
		t.Fatal(err)
	}
	if err := w.st.PutLabel(ctx, w.ids[2], "category", `"not_an_option"`, store.LabelManual); err != nil {
		t.Fatal(err)
	}
	// ids[3] has the fixture's judge label; remove it so the message has no
	// judge at all.
	if err := w.st.DeleteLabel(ctx, w.ids[3], "category", store.LabelJudge); err != nil {
		t.Fatal(err)
	}
	counts, err := w.st.ReviewCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := w.r.LabelQueue(ctx, LabelQuery{Question: "category", Limit: LabelQueueMax, Review: true})
	if err != nil {
		t.Fatal(err)
	}
	if counts["category"] != 2 || queue.Total != counts["category"] {
		t.Fatalf("count %d, queue total %d, want both 2", counts["category"], queue.Total)
	}
	want := map[int64]bool{w.ids[0]: true, w.ids[1]: true}
	if len(queue.Tasks) != len(want) {
		t.Fatalf("queue has %d tasks, want %d", len(queue.Tasks), len(want))
	}
	for _, task := range queue.Tasks {
		if !want[task.MessageID] {
			t.Fatalf("queue offers message %d, outside the count population", task.MessageID)
		}
		if task.MessageID == w.ids[1] && (task.Suggestion != nil || task.SuggestionSource != "") {
			t.Fatalf("the unreadable judge task carries %T %q", task.Suggestion, task.SuggestionSource)
		}
		if task.MessageID == w.ids[0] && task.Suggestion == nil {
			t.Fatal("the readable judge task lost its suggestion")
		}
	}
}

// An unreadable judge value still forms a review task: the badge counts it
// and the queue offers it without a suggestion.
func TestReviewQueueOffersAnUndecodableJudgeLabel(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1, nil)
	ctx := t.Context()
	if err := w.st.PutLabel(ctx, w.ids[0], "category", `"not_an_option"`, store.LabelJudge); err != nil {
		t.Fatal(err)
	}
	queue, err := w.r.LabelQueue(ctx, LabelQuery{Question: "category", Limit: LabelQueueMax, Review: true})
	if err != nil {
		t.Fatal(err)
	}
	if queue.Total != 1 || len(queue.Tasks) != 1 {
		t.Fatalf("review offers %d tasks (total %d), want the one unreadable judge label", len(queue.Tasks), queue.Total)
	}
	task := queue.Tasks[0]
	if task.MessageID != w.ids[0] || task.Question != "category" || task.Reason != LabelReasonReview {
		t.Fatalf("review task %+v", task)
	}
	if task.Suggestion != nil || task.SuggestionSource != "" {
		t.Fatalf("unreadable judge task carries %T %q", task.Suggestion, task.SuggestionSource)
	}
}

// A manual answer of any form suppresses the task: an unreadable manual
// value still means the owner has answered, and the badge agrees.
func TestReviewQueueSkipsAnyManualAnswer(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 1, nil)
	ctx := t.Context()
	if err := w.st.PutLabel(ctx, w.ids[0], "category", `"not_an_option"`, store.LabelManual); err != nil {
		t.Fatal(err)
	}
	counts, err := w.st.ReviewCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := w.r.LabelQueue(ctx, LabelQuery{Question: "category", Limit: LabelQueueMax, Review: true})
	if err != nil {
		t.Fatal(err)
	}
	if counts["category"] != 0 || queue.Total != 0 || len(queue.Tasks) != 0 {
		t.Fatalf("count %d, queue total %d, tasks %d, want no review", counts["category"], queue.Total, len(queue.Tasks))
	}
}

// Review suggestions come from judge labels, so a manual-only requirement
// counts zero until the owner accepts one; accepting removes the message
// from review and moves the labels stage by one.
func TestLabelQueueReviewFeedsAManualOnlyStage(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 12, nil)
	labelRequirements(t, w, 3, []string{store.LabelManual})
	st, err := w.r.LabelsStage().Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if b, ok := blockerOf(st, "category"); !ok || b.Measure == nil || b.Measure.Have != 0 {
		t.Fatalf("category before review: %+v", b)
	}
	query := LabelQuery{Question: "category", Limit: LabelQueueMax, Review: true}
	review, err := w.r.LabelQueue(t.Context(), query)
	if err != nil || len(review.Tasks) != 4 {
		t.Fatalf("review offers %d suggestions, %v", len(review.Tasks), err)
	}
	if err := w.st.PutLabel(t.Context(), review.Tasks[0].MessageID, "category", `"work"`, store.LabelManual); err != nil {
		t.Fatal(err)
	}
	after, err := w.r.LabelQueue(t.Context(), query)
	if err != nil || len(after.Tasks) != 3 {
		t.Fatalf("review offers %d suggestions after one accept, %v", len(after.Tasks), err)
	}
	st, err = w.r.LabelsStage().Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if b, ok := blockerOf(st, "category"); !ok || b.Measure == nil || b.Measure.Have != 1 {
		t.Fatalf("category after one manual label: %+v", b)
	}
}

// index is the member position of a message row (-1: not a member).
func (w *world) index(id int64) int {
	for i, member := range w.ids {
		if member == id {
			return i
		}
	}
	return -1
}

// The queue reports the whole population and whether the returned page is a
// prefix; the label screen uses it instead of claiming completion after one
// page.
func TestLabelQueueReportsTheRemainingPopulation(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 25, nil)
	all, err := w.r.LabelQueue(t.Context(), LabelQuery{Question: "category", Limit: LabelQueueMax, Strategy: LabelStrategyMissing})
	if err != nil {
		t.Fatal(err)
	}
	want := all.Total
	if want < 2 || all.HasMore || len(all.Tasks) != want {
		t.Fatalf("full queue: total=%d has_more=%v tasks=%d", all.Total, all.HasMore, len(all.Tasks))
	}
	page, err := w.r.LabelQueue(t.Context(), LabelQuery{Question: "category", Limit: want - 1, Strategy: LabelStrategyMissing})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != want || !page.HasMore || len(page.Tasks) != want-1 {
		t.Fatalf("page: total=%d has_more=%v tasks=%d want=%d", page.Total, page.HasMore, len(page.Tasks), want)
	}
}

// A label write changes which tasks remain, never their ranking: the next
// queue reuses the same order instead of rescanning the mailbox and
// re-decoding every route answer.
func TestLabelQueueReusesTheOrderAcrossALabelWrite(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 6, nil)
	counted := &queueInputs{Inputs: w.r.in}
	w.r.in = counted
	if _, err := w.r.LabelQueue(t.Context(), LabelQuery{Question: "category", Limit: LabelQueueMax}); err != nil {
		t.Fatal(err)
	}
	if got := counted.builds(); got != 1 {
		t.Fatalf("the first queue read the members %d times, want 1", got)
	}
	if err := w.st.PutLabel(t.Context(), w.ids[0], "category", `"work"`, store.LabelManual); err != nil {
		t.Fatal(err)
	}
	queue, err := w.r.LabelQueue(t.Context(), LabelQuery{Question: "category", Limit: LabelQueueMax})
	if err != nil {
		t.Fatal(err)
	}
	// The queue is keyed by the label generation, so the label write rebuilds
	// it; the ranking is keyed by the mail and route generations only, so the
	// rebuild must reuse it instead of rescanning the population.
	if got := counted.builds(); got != 1 {
		t.Fatalf("a label write re-ranked the queue: %d member reads, want 1", got)
	}
	for _, task := range queue.Tasks {
		if task.MessageID == w.ids[0] {
			t.Fatal("the saved message stayed in the queue")
		}
	}
}

// TestQueueOrderIsOneBuildForConcurrentCallers pins the ranking cache's single
// flight: concurrent callers of the same order share one build, and every one
// of them receives it. It used to be a hand-rolled latch in labels_queue.go;
// viewcache is the one owner of that contract.
func TestQueueOrderIsOneBuildForConcurrentCallers(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 6, nil)
	counted := &queueInputs{Inputs: w.r.in}
	w.r.in = counted
	plan, err := NewPlan(w.r.cfg)
	if err != nil {
		t.Fatal(err)
	}
	question, ok := plan.Profile.Question("category")
	if !ok {
		t.Fatal("the profile has no category question")
	}

	const callers = 8
	// One cold batch: eight callers, one build.
	batch := func(wantBuilds int) {
		t.Helper()
		results := make([][]LabelTask, callers)
		errs := make([]error, callers)
		begin := make(chan struct{})
		var wg sync.WaitGroup
		for i := range callers {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-begin
				results[i], errs[i] = w.r.queueOrder(t.Context(), plan, question, LabelStrategyUncertainty)
			}(i)
		}
		close(begin)
		wg.Wait()
		if got := counted.builds(); got != wantBuilds {
			t.Fatalf("%d member reads, want %d", got, wantBuilds)
		}
		for i := range callers {
			if errs[i] != nil {
				t.Fatalf("caller %d: %v", i, errs[i])
			}
			if len(results[i]) != len(results[0]) {
				t.Fatalf("caller %d got %d tasks, caller 0 got %d", i, len(results[i]), len(results[0]))
			}
			for j := range results[i] {
				if results[i][j].MessageID != results[0][j].MessageID {
					t.Fatalf("caller %d order differs from caller 0", i)
				}
			}
		}
	}
	batch(1) // the cold batch shares one build
	batch(1) // a warm batch reads nothing
}
