package layer1

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/route"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/viewcache"
)

// queueInputs counts the queue builds that actually read the members.
type queueInputs struct {
	Inputs
	mu        sync.Mutex
	memberIDs int
}

func (c *queueInputs) MemberIDs(ctx context.Context) ([]int64, error) {
	c.mu.Lock()
	c.memberIDs++
	c.mu.Unlock()
	return c.Inputs.MemberIDs(ctx)
}

func (c *queueInputs) builds() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.memberIDs
}

// A repeated query (with the defaults spelled out) is served from the cache;
// a collect call reads the members once.
func TestLabelQueueCacheServesRepeatedQueries(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 6, nil)
	counted := &queueInputs{Inputs: w.r.in}
	w.r.in = counted
	first, err := w.r.LabelQueue(t.Context(), LabelQuery{Question: "category"})
	if err != nil || len(first.Tasks) == 0 {
		t.Fatalf("queue %+v %v", first, err)
	}
	same, err := w.r.LabelQueue(t.Context(), LabelQuery{Question: "category", Limit: LabelQueueDefault, Strategy: LabelStrategyAuto})
	if err != nil {
		t.Fatal(err)
	}
	if &first.Tasks[0] != &same.Tasks[0] {
		t.Fatal("the defaults spelled out did not reuse the cached queue")
	}
	if got := counted.builds(); got != 1 {
		t.Fatalf("%d builds for one queue, want 1", got)
	}
	if n := w.r.queueCache().Len(); n != 1 {
		t.Fatalf("%d cache entries for one queue, want 1", n)
	}
}

// The strategy and the review flag are part of the key: each renders its own
// queue instead of serving the other one's.
func TestLabelQueueCacheSeparatesStrategyAndReview(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 6, nil)
	uncertain, err := w.r.LabelQueue(t.Context(), LabelQuery{Question: "category", Limit: LabelQueueMax})
	if err != nil {
		t.Fatal(err)
	}
	missing, err := w.r.LabelQueue(t.Context(), LabelQuery{Question: "category", Limit: LabelQueueMax, Strategy: LabelStrategyMissing})
	if err != nil {
		t.Fatal(err)
	}
	review, err := w.r.LabelQueue(t.Context(), LabelQuery{Question: "category", Limit: LabelQueueMax, Review: true})
	if err != nil {
		t.Fatal(err)
	}
	if n := w.r.queueCache().Len(); n != 3 {
		t.Fatalf("%d cache entries for three distinct queries, want 3", n)
	}
	if len(missing.Tasks) != len(uncertain.Tasks) {
		t.Fatalf("the strategies see different populations: %d vs %d", len(missing.Tasks), len(uncertain.Tasks))
	}
	if &missing.Tasks[0] == &uncertain.Tasks[0] {
		t.Fatal("the missing strategy served the uncertainty queue")
	}
	if len(review.Tasks) == 0 || review.Tasks[0].Reason != LabelReasonReview {
		t.Fatalf("review queue %+v", review)
	}
	for i := 1; i < len(missing.Tasks); i++ {
		if missing.Tasks[i].MessageID <= missing.Tasks[i-1].MessageID {
			t.Fatalf("missing order at %d: %+v", i, missing.Tasks)
		}
	}
}

// A label write invalidates the cached queue: the labelled message is gone
// from the next answer, without a new process or a restart.
func TestLabelQueueCacheFollowsLabelWrites(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 6, nil)
	before, err := w.r.LabelQueue(t.Context(), LabelQuery{Question: "category", Limit: LabelQueueMax})
	if err != nil || len(before.Tasks) == 0 {
		t.Fatalf("queue %+v %v", before, err)
	}
	target := before.Tasks[0].MessageID
	if err := w.st.PutLabel(t.Context(), target, "category", `"work"`, store.LabelManual); err != nil {
		t.Fatal(err)
	}
	after, err := w.r.LabelQueue(t.Context(), LabelQuery{Question: "category", Limit: LabelQueueMax})
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Tasks) != len(before.Tasks)-1 {
		t.Fatalf("the queue offers %d tasks after one manual label, want %d", len(after.Tasks), len(before.Tasks)-1)
	}
	for _, task := range after.Tasks {
		if task.MessageID == target {
			t.Fatal("a labelled message is still offered")
		}
	}
}

// A layer-1 commit that rewrites the route answers invalidates the cached
// queue: the uncertainty order follows the new confidences.
func TestLabelQueueCacheFollowsRouteCommits(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 6, nil)
	before, err := w.r.LabelQueue(t.Context(), LabelQuery{Question: "category", Limit: LabelQueueMax})
	if err != nil || len(before.Tasks) < 2 {
		t.Fatalf("queue %+v %v", before, err)
	}
	// Give every offered message a route answer whose confidence descends
	// with the id, so the uncertainty order is the reverse of the id order
	// the queue serves now.
	ordered := make([]int64, len(before.Tasks))
	for i, task := range before.Tasks {
		ordered[i] = task.MessageID
	}
	confidences := make(map[int64]float64, len(ordered))
	for i, id := range ordered {
		confidences[id] = 0.99 - float64(i)*0.05
	}
	gen := w.st.Generation()
	commitCategoryConfidence(t, w, confidences)
	if got := w.st.Generation(); got != gen {
		t.Fatalf("the route commit moved the mailbox generation: %d → %d", gen, got)
	}
	after, err := w.r.LabelQueue(t.Context(), LabelQuery{Question: "category", Limit: LabelQueueMax})
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Tasks) != len(before.Tasks) {
		t.Fatalf("the route commit changed the population: %d → %d", len(before.Tasks), len(after.Tasks))
	}
	if after.Tasks[0].MessageID != ordered[len(ordered)-1] {
		t.Fatalf("the queue did not follow the committed answer: first %d, want the lowest confidence %d",
			after.Tasks[0].MessageID, ordered[len(ordered)-1])
	}
}

// commitCategoryConfidence replaces the route answers of the world's members
// with one category answer each (confidence from the map), in one layer-1
// commit.
func commitCategoryConfidence(t *testing.T, w *world, confidence map[int64]float64) {
	t.Helper()
	run, err := w.st.BeginLayer1Run(t.Context(), "queue-cache")
	if err != nil {
		t.Fatal(err)
	}
	q, ok := w.p.Question("category")
	if !ok {
		t.Fatal("category")
	}
	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	err = w.st.CommitLayer1(t.Context(), store.Layer1Commit{
		RunID: run, Report: func() (string, error) { return "{}", nil },
		Key:       store.TriageKey{Fingerprint: w.p.Fingerprint, Decider: Decider},
		RouteKey:  plan.RouteKey(),
		Decisions: func(fn func(store.Decision) error) error { return nil },
		Routes: func(fn func(store.RouteAnswer) error) error {
			for id, c := range confidence {
				conf := c
				a := map[string]route.Answer{q.Name: {Answer: engine.Answer{Type: q.Type, Choice: q.Options[0].Name,
					Probabilities: map[string]float64{q.Options[0].Name: c}, Confidence: &conf}}}
				raw, err := json.Marshal(a)
				if err != nil {
					return err
				}
				if err := fn(store.RouteAnswer{MessageID: id, Revision: w.routeRevision(t, id), Answers: string(raw)}); err != nil {
					return err
				}
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

// routeRevision is a message row's current revision.
func (w *world) routeRevision(t *testing.T, id int64) int64 {
	t.Helper()
	var rev int64
	if err := w.st.EachMessage(t.Context(), store.MessageFilter{RowIDs: []int64{id}}, func(r *store.MessageRow) error {
		rev = r.Revision
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return rev
}

// Concurrent callers of one missing queue share its one build.
func TestLabelQueueCacheSharesOneBuild(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 6, nil)
	counted := &queueInputs{Inputs: w.r.in}
	w.r.in = counted
	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	key := w.r.queueKeyOf(plan.RouteKey(), "category", LabelQueueMax, LabelStrategyUncertainty, false)
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	var builds atomic.Int64
	build := func(context.Context) (LabelQueue, error) {
		builds.Add(1)
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return LabelQueue{Question: "category", Strategy: LabelStrategyUncertainty, Tasks: []LabelTask{{MessageID: 1}}}, nil
	}
	const n = 8
	var wg sync.WaitGroup
	out := make([]LabelQueue, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i], errs[i] = w.r.labelQueue(context.Background(), key, build)
		}()
	}
	<-started
	close(release)
	wg.Wait()
	if got := builds.Load(); got != 1 {
		t.Fatalf("%d builds for one missing queue, want 1", got)
	}
	for i := range n {
		if errs[i] != nil || len(out[i].Tasks) != 1 {
			t.Fatalf("caller %d: %+v %v", i, out[i], errs[i])
		}
	}

	// The same key through the public call path is now a fresh hit: no
	// member read at all.
	if _, err := w.r.LabelQueue(t.Context(), LabelQuery{Question: "category", Limit: LabelQueueMax}); err != nil {
		t.Fatal(err)
	}
	if got := counted.builds(); got != 0 {
		t.Fatalf("a fresh queue read the members: %d reads", got)
	}
	// The shared build's queue is what the cache serves: a read through the
	// public path returns it without running the build again.
	served, err := w.r.labelQueue(context.Background(), key, func(context.Context) (LabelQueue, error) {
		t.Error("the cached queue was not served")
		return LabelQueue{}, nil
	})
	if err != nil || len(served.Tasks) != 1 || served.Tasks[0].MessageID != 1 {
		t.Fatalf("the cached queue = %+v, %v", served, err)
	}
}

// A route-key input that is not a file stamp and has not committed yet must
// still invalidate the queue: the uncertainty order was computed under the
// old key.
func TestQueueKeyFollowsTheRouteKey(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 6, nil)
	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	before := w.r.queueKeyOf(plan.RouteKey(), "category", LabelQueueMax, LabelStrategyUncertainty, false)
	if err := w.cfg.Set("evidence.rules.z", 2.5); err != nil {
		t.Fatal(err)
	}
	plan, err = NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	after := w.r.queueKeyOf(plan.RouteKey(), "category", LabelQueueMax, LabelStrategyUncertainty, false)
	if before == after {
		t.Fatal("the queue key does not follow the route key")
	}
}

// A label click rebuilds the queue but not the member population: the ids
// come from mail and folder writes, which bump MailGeneration. A mail write
// still produces a fresh scan.
func TestMemberIDsSurviveALabelWriteAndFollowMail(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 6, nil)
	in := &StoreInputs{Config: w.cfg, Store: w.st}
	first, err := in.MemberIDs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(first) == 0 {
		t.Fatal("the fixture has no members")
	}
	if err := w.st.DeleteLabel(t.Context(), 1, "category", "manual"); err != nil {
		t.Fatal(err)
	}
	same, err := in.MemberIDs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if &same[0] != &first[0] {
		t.Fatal("a label write rescanned the member population")
	}
	if err := w.st.SaveReported(t.Context(), store.StoreRef{SourceID: "src", StoreID: "S1"},
		[]string{"owner@example.test"}); err != nil {
		t.Fatal(err)
	}
	after, err := in.MemberIDs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if &after[0] == &first[0] {
		t.Fatal("a mail write reused the member population")
	}
}

// TestClosedLabelQueueCacheStopsReadingTheStore: the queue cache used to be a
// private copy with a detached rebuild that nothing joined, so a queue read
// could still reach the store while its owner was closing it. It is the shared
// viewcache now, whose one rule after Close is: an installed queue is still
// served, and a key with nothing installed is refused instead of built.
func TestClosedLabelQueueCacheStopsReadingTheStore(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 6, nil)
	warm, err := w.r.LabelQueue(t.Context(), LabelQuery{Question: "category", Limit: LabelQueueMax})
	if err != nil || len(warm.Tasks) == 0 {
		t.Fatalf("queue %+v %v", warm, err)
	}
	if !w.r.Close(t.Context()) {
		t.Fatal("Close reported a rebuild it could not join")
	}
	// An installed queue is the whole answer after Close: no store read.
	served, err := w.r.LabelQueue(t.Context(), LabelQuery{Question: "category", Limit: LabelQueueMax})
	if err != nil {
		t.Fatalf("the installed queue was not served after Close: %v", err)
	}
	if len(served.Tasks) != len(warm.Tasks) {
		t.Fatalf("the served queue changed after Close: %d → %d tasks", len(warm.Tasks), len(served.Tasks))
	}
	// A key with nothing installed must be refused, not built.
	_, err = w.r.labelQueue(t.Context(), queueKey{question: "uncached", limit: LabelQueueMax}, func(context.Context) (LabelQueue, error) {
		t.Error("the queue cache read the store after Close")
		return LabelQueue{}, nil
	})
	if !errors.Is(err, viewcache.ErrClosed) {
		t.Fatalf("a missing key after Close = %v, want ErrClosed", err)
	}
}
