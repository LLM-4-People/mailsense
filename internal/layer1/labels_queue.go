package layer1

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/selective"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// Label queue bounds and strategies. They are part of the action's
// contract, not settings: the UI asks for a page and the server refuses a
// page it would not render.
const (
	// LabelQueueDefault is the page size of a query without one.
	LabelQueueDefault = 20
	// LabelQueueMax is the most tasks one queue returns.
	LabelQueueMax = 200

	// LabelStrategyAuto and LabelStrategyUncertainty rank by the route's
	// confidence closest to its decision boundary, abstained messages first
	// (the default).
	LabelStrategyAuto        = "auto"
	LabelStrategyUncertainty = "uncertainty"
	// LabelStrategyMissing ranks the label-free messages by id.
	LabelStrategyMissing = "missing"

	// LabelReasonUncertain, LabelReasonMissing and LabelReasonReview are
	// the closed reasons a task carries (never a prediction, threshold or,
	// outside review mode, a label value).
	LabelReasonUncertain = "uncertain"
	LabelReasonMissing   = "missing"
	LabelReasonReview    = "review"
)

// ErrUnknownQuestion is returned for a question the profile does not name.
var ErrUnknownQuestion = errors.New("layer1: unknown question")

// LabelQuery asks which messages to label for one question.
type LabelQuery struct {
	Question string `json:"question"`
	Limit    int    `json:"limit,omitempty"`    // 0: LabelQueueDefault
	Strategy string `json:"strategy,omitempty"` // "", auto or uncertainty (default), or missing
	// Review offers messages that carry a judge label for the question and
	// no manual label, with the judge's decoded suggestion when the stored
	// value decodes.
	Review bool `json:"review,omitempty"`
}

// LabelTask is one message the owner should label. It carries no content:
// the UI reads the message through its own gated path. In review mode
// Suggestion is the decoded judge value the answer controls apply (option
// name, yes/no, or level index) and SuggestionSource names the source it
// came from; a judge value that does not decode yields the task without
// either field, and a normal task carries neither.
type LabelTask struct {
	MessageID        int64  `json:"message_id"`
	Question         string `json:"question"`
	Reason           string `json:"reason"`
	Suggestion       any    `json:"suggestion,omitempty"`
	SuggestionSource string `json:"suggestion_source,omitempty"`
}

// LabelQueue is the deterministic answer of a label query. It carries no
// message content; a review task additionally carries the decoded judge
// suggestion when the stored value decodes.
type LabelQueue struct {
	Question string      `json:"question"`
	Strategy string      `json:"strategy"`
	Tasks    []LabelTask `json:"tasks"`
	// Total is the whole candidate population at the query's moment and
	// HasMore says the page is a prefix of it. The label screen must not
	// claim the queue is complete when the server only returned a page.
	Total   int  `json:"total"`
	HasMore bool `json:"has_more"`
}

// LabelQueue lists messages of the routed, labelable population that the
// owner should label for the question. Without Review it offers only
// messages with no evaluation label for the question yet; the default
// uncertainty strategy puts abstained messages first, then those whose
// route answer is closest to its decision boundary (lowest confidence),
// with the id as the deterministic tie-break; the missing strategy orders
// by id. That mode never reads a label value, makes no paid call and
// returns no message content.
//
// With Review it instead offers the visible members that carry a judge
// label for the question and no manual label, so the owner can accept or
// correct the teacher's suggestion. It intentionally reads that stored
// judge value and returns its decoded form (Suggestion) when it decodes
// under the current profile; an unreadable judge value still yields a task,
// without a suggestion, rather than failing the queue or hiding the task
// the badge counts. Review still returns no message content and keeps the
// normal limits, strategies and deterministic ordering.
//
// The queue is served from the bounded read-model cache (labels_cache.go):
// a fresh entry answers at once, an expired one answers while one
// background rebuild runs, and concurrent first callers share a single
// build. A label write or a route commit changes the key, so the next call
// never serves the population of the previous one.
func (r *Runner) LabelQueue(ctx context.Context, query LabelQuery) (LabelQueue, error) {
	limit := query.Limit
	if limit == 0 {
		limit = LabelQueueDefault
	}
	if limit < 0 || limit > LabelQueueMax {
		return LabelQueue{Question: query.Question}, fmt.Errorf("layer1: label queue: limit must be 0 (default) or between 1 and %d, got %d", LabelQueueMax, query.Limit)
	}
	strategy := query.Strategy
	switch strategy {
	case "", LabelStrategyAuto, LabelStrategyUncertainty:
		strategy = LabelStrategyUncertainty
	case LabelStrategyMissing:
	default:
		return LabelQueue{Question: query.Question}, fmt.Errorf("layer1: label queue: unknown strategy %q", query.Strategy)
	}
	// The key is composed from the memoized route key and the store
	// generations, so a warm hit never builds a Plan. Loading the profile,
	// teachers, requirements and catalog is real work; it belongs to the
	// miss that actually renders a queue.
	routeKey, err := RouteKey(r.cfg)
	if err != nil {
		return LabelQueue{Question: query.Question}, err
	}
	key := r.queueKeyOf(routeKey, query.Question, limit, strategy, query.Review)
	queue, err := r.labelQueue(ctx, key, func(ctx context.Context) (LabelQueue, error) {
		plan, err := NewPlan(r.cfg)
		if err != nil {
			return LabelQueue{Question: query.Question}, err
		}
		return r.buildLabelQueue(ctx, plan, query.Question, limit, strategy, query.Review)
	})
	if err != nil {
		if queue.Question == "" {
			queue.Question = query.Question
		}
		return queue, err
	}
	return queue, nil
}

// QueueContains reports whether the label queue of one normalized query
// offers this message. The pre-gate label read is bound to it: reading the
// mail a task asks about must not unlock any other message. The queue is the
// cached one the label screen just read, so the check costs a scan of that
// page, not a second build.
func (r *Runner) QueueContains(ctx context.Context, query LabelQuery, messageID int64) (bool, error) {
	if messageID <= 0 {
		return false, nil
	}
	queue, err := r.LabelQueue(ctx, query)
	if err != nil {
		return false, err
	}
	for _, task := range queue.Tasks {
		if task.MessageID == messageID {
			return true, nil
		}
	}
	return false, nil
}

// queueKeyOf is the cache key of a normalized query: the one owner of
// the queue key's components.
func (r *Runner) queueKeyOf(routeKey, question string, limit int, strategy string, review bool) queueKey {
	return queueKey{question: question, limit: limit, strategy: strategy, review: review,
		version:  r.cfg.VersionKey(taxonomy.ProfileFileSetting, SettingTeachersFile, SettingRequirementsFile, engine.ProvidersFileSetting),
		routeKey: routeKey,
		gen:      r.st.Generation(),
		routeGen: r.st.RouteGeneration()}
}

// orderKey identifies one ranked candidate list. Labels are deliberately
// absent: the rank is computed from the member population and the stored
// route answers, and the current labels filter that list at read time. A
// label click therefore reuses the ranking instead of rescanning the
// mailbox and re-decoding every route answer.
type orderKey struct {
	question string
	strategy string
	version  string
	routeKey string
	mailGen  uint64
	routeGen uint64
}

// labelOrderTTL bounds the staleness a generation cannot see, exactly as
// labelQueueTTL does for the queue: the generations in orderKey carry
// correctness, so an old ranking is never served for a changed mailbox or
// route table.
const labelOrderTTL = labelQueueTTL

// labelOrderSize is the entry bound of the ranking cache: four entries cover
// the questions a reviewer cycles through.
const labelOrderSize = 4

// queueOrder returns the ranked candidates of one question. Every concurrent
// caller of a missing order shares one build (viewcache is the single-flight
// owner); a warm one is shared by every caller.
func (r *Runner) queueOrder(ctx context.Context, plan *Plan, question *engine.Question, strategy string) ([]LabelTask, error) {
	key := orderKey{question: question.Name, strategy: strategy,
		version:  r.cfg.VersionKey(taxonomy.ProfileFileSetting, SettingTeachersFile, SettingRequirementsFile, engine.ProvidersFileSetting),
		routeKey: plan.RouteKey(), mailGen: r.st.MailGeneration(), routeGen: r.st.RouteGeneration()}
	return r.orderCache().Get(ctx, key, func(buildCtx context.Context) ([]LabelTask, error) {
		return r.buildOrder(buildCtx, plan, question, strategy)
	})
}

// buildOrder ranks every member once for a question. The uncertainty order
// reads the stored route answers; the missing order is id order. A label
// never enters this build.
func (r *Runner) buildOrder(ctx context.Context, plan *Plan, question *engine.Question, strategy string) ([]LabelTask, error) {
	memberIDs, err := r.in.MemberIDs(ctx)
	if err != nil {
		return nil, err
	}
	reason := LabelReasonMissing
	if strategy == LabelStrategyUncertainty {
		reason = LabelReasonUncertain
	}
	type candidate struct {
		id         int64
		abstained  bool
		confidence float64
	}
	candidates := make([]candidate, 0, len(memberIDs))
	for _, id := range memberIDs {
		candidates = append(candidates, candidate{id: id})
	}
	if strategy == LabelStrategyUncertainty {
		answers, err := r.st.LoadRouteAnswers(ctx, plan.RouteKey(), memberIDs)
		if err != nil {
			return nil, err
		}
		cutoff := plan.Profile.Cutoff(question.Name)
		for i := range candidates {
			a, ok := answers[candidates[i].id][question.Name]
			if !ok {
				candidates[i].abstained = true
				continue
			}
			confidence, given := selective.Confidence(a.Answer, cutoff)
			candidates[i].abstained = !given
			candidates[i].confidence = confidence
		}
		slices.SortFunc(candidates, func(a, b candidate) int {
			if a.abstained != b.abstained {
				if a.abstained {
					return -1
				}
				return 1
			}
			if c := cmp.Compare(a.confidence, b.confidence); c != 0 {
				return c
			}
			return cmp.Compare(a.id, b.id)
		})
	} else {
		slices.SortFunc(candidates, func(a, b candidate) int { return cmp.Compare(a.id, b.id) })
	}
	tasks := make([]LabelTask, len(candidates))
	for i, c := range candidates {
		tasks[i] = LabelTask{MessageID: c.id, Question: question.Name, Reason: reason}
	}
	return tasks, nil
}

// buildLabelQueue renders one queue from the plan and the store. It is the
// build behind the cache and must not touch it.
func (r *Runner) buildLabelQueue(ctx context.Context, plan *Plan, name string, limit int, strategy string, review bool) (LabelQueue, error) {
	out := LabelQueue{Question: name}
	question, ok := plan.Profile.Question(name)
	if !ok {
		return out, fmt.Errorf("%w: %q", ErrUnknownQuestion, name)
	}
	out.Strategy = strategy
	rows, err := r.in.Labels(ctx)
	if err != nil {
		return out, err
	}
	labels := labelIndex(rows)
	if review {
		return r.buildReviewQueue(ctx, out, question, plan.Profile.Cutoff(question.Name), labels, limit)
	}
	order, err := r.queueOrder(ctx, plan, question, strategy)
	if err != nil {
		return out, err
	}
	tasks := make([]LabelTask, 0, len(order))
	for _, task := range order {
		if labelMissing(plan, question, labels, task.MessageID) {
			tasks = append(tasks, task)
		}
	}
	out.Total = len(tasks)
	if len(tasks) > limit {
		tasks = tasks[:limit]
		out.HasMore = true
	}
	out.Tasks = tasks
	return out, nil
}

// buildReviewQueue renders the review population: one task per visible owner
// member that carries a judge label for the question and no manual label.
// Decodability does not filter the population; it only decides whether the
// task carries a suggestion. The build decodes stored judge values, so it is
// keyed by the label generation through the queue cache and is not part of
// the label-independent order.
func (r *Runner) buildReviewQueue(ctx context.Context, out LabelQueue, question *engine.Question, cutoff float64, labels map[string]map[int64]map[string]string, limit int) (LabelQueue, error) {
	memberIDs, err := r.in.MemberIDs(ctx)
	if err != nil {
		return out, err
	}
	type candidate struct {
		id            int64
		suggestion    any
		hasSuggestion bool
	}
	candidates := make([]candidate, 0, len(memberIDs))
	for _, id := range memberIDs {
		suggestion, hasSuggestion, offer := reviewCandidate(question, labels, id, cutoff)
		if !offer {
			continue
		}
		candidates = append(candidates, candidate{id: id, suggestion: suggestion, hasSuggestion: hasSuggestion})
	}
	out.Total = len(candidates)
	if len(candidates) > limit {
		candidates = candidates[:limit]
		out.HasMore = true
	}
	out.Tasks = make([]LabelTask, len(candidates))
	for i, c := range candidates {
		task := LabelTask{MessageID: c.id, Question: question.Name, Reason: LabelReasonReview}
		if c.hasSuggestion {
			task.Suggestion = c.suggestion
			task.SuggestionSource = store.LabelJudge
		}
		out.Tasks[i] = task
	}
	return out, nil
}

// labelMissing reports whether a member has no evaluation label of q: the
// manual queue's population rule (the first evaluation source with a value
// valid under the current profile). The judge run has its own rule because
// it also has to consider the prompt an existing label came from.
func labelMissing(plan *Plan, q *engine.Question, labels map[string]map[int64]map[string]string, id int64) bool {
	_, ok := evalOf(q, plan.Teachers.For(q.Name), labels, id, plan.Profile.Cutoff(q.Name))
	return !ok
}

// reviewCandidate is the review queue's one population rule and its decoded
// judge suggestion. A task exists iff the member carries a judge label for q
// and no manual label for the same message and question, regardless of
// whether either value decodes under the current profile: a manual answer of
// any form means the owner has answered.
//
// Decodability only decides the suggestion. An unreadable judge value is
// still a task, offered without one, never a queue error. When it decodes,
// the value is the primitive the answer controls apply: the option name, the
// yes/no side at the profile's cutoff, or the level index (rounded half-up,
// the label's class).
func reviewCandidate(q *engine.Question, labels map[string]map[int64]map[string]string, id int64, cutoff float64) (suggestion any, hasSuggestion, offer bool) {
	if _, ok := labels[store.LabelManual][id][q.Name]; ok {
		return nil, false, false
	}
	raw, ok := labels[store.LabelJudge][id][q.Name]
	if !ok {
		return nil, false, false
	}
	label, err := taxonomy.DecodeLabel(q, raw)
	if err != nil {
		return nil, false, true
	}
	class := labelClass(q, label, cutoff)
	switch q.Type {
	case engine.Choice:
		return label.Choice, true, true
	case engine.Noul:
		return class == 1, true, true
	}
	return float64(class), true, true
}
