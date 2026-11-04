package layer1

import (
	"context"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/viewcache"
)

// Label-queue cache bounds. The generations and version in a queueKey carry
// correctness: a mail, label, route-answer, profile, teacher or requirement
// change produces another key, so an old queue is never served for it. The
// TTL only bounds the staleness a generation cannot see (a row another process
// wrote, a file rewritten under the same stamp), and the entry bound keeps the
// memory of a labelling session small. The single-flight, stale-while-
// revalidate behaviour, the detached rebuild and its shutdown join are
// viewcache's: every read service uses that one cache instead of its own copy.
const (
	labelQueueTTL  = 30 * time.Second
	labelQueueSize = 16
)

// queueKey identifies one rendered label queue. Every input that changes
// the result is part of it: the question, the effective limit and strategy
// (after the defaults), the review flag, the config version of every file
// the plan reads, the route key the uncertainty order was computed under,
// the store generation (labels and mail) and the route generation (route
// answers). The route key is explicit: an evidence-rules edit is a key input
// but is not in the config version's file stamps and does not bump the route
// generation until a commit.
type queueKey struct {
	question string
	limit    int
	strategy string
	review   bool
	version  string
	routeKey string
	gen      uint64
	routeGen uint64
}

// queueCache is the bounded label-queue view cache. It is built on first use
// rather than in New, so a Runner constructed as a literal (several tests, and
// every surface that never opens the queue) still works.
func (r *Runner) queueCache() *viewcache.Cache[queueKey, LabelQueue] {
	r.queuesOnce.Do(func() {
		r.queues = viewcache.New(viewcache.Options[queueKey, LabelQueue]{
			TTL:  labelQueueTTL,
			Size: labelQueueSize,
			Now:  clock.Or(r.clock).Now,
			// The rebuild timeout is viewcache's default, not restated here.
			Component: "layer1",
		})
	})
	return r.queues
}

// labelQueue returns the queue of key. A fresh entry is returned at once, a
// missing one is built here exactly once for every concurrent caller, and an
// expired one is served at once while one detached rebuild runs.
func (r *Runner) labelQueue(ctx context.Context, key queueKey, build func(context.Context) (LabelQueue, error)) (LabelQueue, error) {
	return r.queueCache().Get(ctx, key, build)
}

// orderCache is the ranking cache behind the queue: the label-independent
// candidate order of one question, keyed by the mailbox and route
// generations. It is the same viewcache as the queue (bounded, single-flight,
// stale-while-revalidate, joined at shutdown); the ranking has its own entry
// bound because a reviewer cycles through a handful of questions.
func (r *Runner) orderCache() *viewcache.Cache[orderKey, []LabelTask] {
	r.ordersOnce.Do(func() {
		r.orders = viewcache.New(viewcache.Options[orderKey, []LabelTask]{
			TTL:       labelOrderTTL,
			Size:      labelOrderSize,
			Now:       clock.Or(r.clock).Now,
			Component: "layer1",
		})
	})
	return r.orders
}

// Close joins the label-queue and ranking rebuilds in flight, bounded by ctx,
// so the store owner never closes the store under a queue read.
func (r *Runner) Close(ctx context.Context) bool {
	queues := r.queueCache().Close(ctx)
	orders := r.orderCache().Close(ctx)
	return queues && orders
}
