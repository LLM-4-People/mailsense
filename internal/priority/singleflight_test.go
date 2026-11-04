package priority

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/route"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// coldRow is one ranked message: a reply obligation is enough to enter the
// ranking without a thread resolver.
func coldRow(id int64) store.PriorityRow {
	return store.PriorityRow{MessageID: id, Subject: "cold", ReceivedAt: receivedAgo(0),
		Route: map[string]route.Answer{"reply_needed": noul("reply_needed", 0.9)}}
}

type coldResult struct {
	items   []Item
	preview bool
	err     error
}

func startRank(ctx context.Context, svc *Service) <-chan coldResult {
	out := make(chan coldResult, 1)
	go func() {
		items, _, preview, err := svc.Rank(ctx, FilterAll, 8)
		out <- coldResult{items: items, preview: preview, err: err}
	}()
	return out
}

func waitRank(t *testing.T, what string, out <-chan coldResult, timeout time.Duration) coldResult {
	t.Helper()
	select {
	case got := <-out:
		return got
	case <-time.After(timeout):
		t.Fatalf("%s did not return within %s", what, timeout)
		return coldResult{}
	}
}

// Two concurrent readers of an empty cache share one build. Before the
// single-flight the second reader started its own scan and discarded the
// first result.
func TestRankingColdBuildIsSingleFlight(t *testing.T) {
	svc, fs := newBlockingService(t, []store.PriorityRow{coldRow(1)}, clocktest.New(testNow))
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	fs.block(started, release)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	first := startRank(context.Background(), svc)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the cold build never reached the store")
	}
	second := startRank(context.Background(), svc)
	time.Sleep(100 * time.Millisecond)
	if got := fs.callCount(); got != 1 {
		close(release)
		t.Fatalf("two concurrent cold readers made %d store scans, want 1", got)
	}
	close(release)

	a := waitRank(t, "the first cold reader", first, 5*time.Second)
	b := waitRank(t, "the second cold reader", second, 5*time.Second)
	if a.err != nil || b.err != nil {
		t.Fatalf("cold readers: %v / %v", a.err, b.err)
	}
	if len(a.items) != 1 || len(b.items) != 1 || a.items[0].MessageID != b.items[0].MessageID {
		t.Fatalf("the callers saw different rankings: %+v / %+v", a.items, b.items)
	}
	if got := fs.callCount(); got != 1 {
		t.Fatalf("the cold build scanned %d times, want 1", got)
	}
}

// A caller that gives up waiting does not cancel the shared build: the next
// caller still gets the one result instead of paying for a second scan.
func TestRankingColdBuildSurvivesCallerCancellation(t *testing.T) {
	svc, fs := newBlockingService(t, []store.PriorityRow{coldRow(1)}, clocktest.New(testNow))
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	fs.block(started, release)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	first := startRank(ctx, svc)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the cold build never reached the store")
	}
	cancel()
	early := waitRank(t, "the cancelled cold reader", first, 5*time.Second)
	if !errors.Is(early.err, context.Canceled) {
		t.Fatalf("the cancelled reader returned %v, want context.Canceled", early.err)
	}

	second := startRank(context.Background(), svc)
	time.Sleep(50 * time.Millisecond)
	close(release)
	got := waitRank(t, "the surviving cold reader", second, 5*time.Second)
	if got.err != nil || len(got.items) != 1 || got.items[0].MessageID != 1 {
		t.Fatalf("the surviving reader got %+v / %v, want the one item", got.items, got.err)
	}
	if count := fs.callCount(); count != 1 {
		t.Fatalf("one cold build after a caller cancellation scanned %d times, want 1", count)
	}
}

// A failed cold build is shared by the readers waiting on it. The next call
// retries: a failure is not cached.
func TestRankingColdBuildFailureIsSharedAndRetried(t *testing.T) {
	svc, fs := newBlockingService(t, []store.PriorityRow{coldRow(1)}, clocktest.New(testNow))
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	fs.block(started, release)
	fs.setErr(errors.New("store offline"))

	first := startRank(context.Background(), svc)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the cold build never reached the store")
	}
	second := startRank(context.Background(), svc)
	time.Sleep(100 * time.Millisecond)
	if got := fs.callCount(); got != 1 {
		close(release)
		t.Fatalf("concurrent failing readers made %d scans, want 1", got)
	}
	close(release)

	a := waitRank(t, "the first failing reader", first, 5*time.Second)
	b := waitRank(t, "the second failing reader", second, 5*time.Second)
	if a.err == nil || b.err == nil {
		t.Fatalf("the shared failure returned %v / %v, want the same error", a.err, b.err)
	}
	if a.err.Error() != b.err.Error() {
		t.Fatalf("the readers saw different errors: %v / %v", a.err, b.err)
	}
	if got := fs.callCount(); got != 1 {
		t.Fatalf("the shared failed build scanned %d times, want 1", got)
	}

	fs.setErr(nil)
	retry := startRank(context.Background(), svc)
	got := waitRank(t, "the retried cold reader", retry, 5*time.Second)
	if got.err != nil || len(got.items) != 1 {
		t.Fatalf("the retry got %+v / %v, want the item", got.items, got.err)
	}
	if count := fs.callCount(); count != 2 {
		t.Fatalf("a failed cold build was not retried once: %d scans, want 2", count)
	}
}

// A caller that switches the preview while the first build runs waits for
// that build to clear, then builds the new preview once. It never starts a
// second scan beside the one already in flight.
func TestRankingColdBuildWaitsForADifferentPreview(t *testing.T) {
	svc, fs := newBlockingService(t, []store.PriorityRow{coldRow(1)}, clocktest.New(testNow))
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	fs.block(started, release)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	first := startRank(context.Background(), svc)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the cold build never reached the store")
	}
	if err := svc.cfg.Set("learning.test_mode", true); err != nil {
		t.Fatal(err)
	}
	second := startRank(context.Background(), svc)
	select {
	case got := <-second:
		close(release)
		t.Fatalf("the different-preview reader returned before the first build finished: %+v", got)
	case <-time.After(100 * time.Millisecond):
	}
	if count := fs.callCount(); count != 1 {
		close(release)
		t.Fatalf("the different-preview reader started a second scan while the first was in flight: %d", count)
	}
	close(release)

	a := waitRank(t, "the first cold reader", first, 5*time.Second)
	if a.err != nil || a.preview || len(a.items) != 1 {
		t.Fatalf("the first reader got %+v / preview=%t / %v", a.items, a.preview, a.err)
	}
	b := waitRank(t, "the different-preview reader", second, 5*time.Second)
	if b.err != nil || !b.preview || len(b.items) != 1 {
		t.Fatalf("the different-preview reader got %+v / preview=%t / %v", b.items, b.preview, b.err)
	}
	if count := fs.callCount(); count != 2 {
		t.Fatalf("the preview switch built %d times, want 2 (one per preview)", count)
	}
}

// A waiter that gives up does not cancel the build it joined: the builder
// completes it, and a later caller reads the cache without another scan.
func TestRankingColdBuildWaiterCancellationDoesNotCancelTheBuild(t *testing.T) {
	svc, fs := newBlockingService(t, []store.PriorityRow{coldRow(1)}, clocktest.New(testNow))
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	fs.block(started, release)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	builder := startRank(context.Background(), svc)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the cold build never reached the store")
	}
	ctx, cancel := context.WithCancel(context.Background())
	waiter := startRank(ctx, svc)
	time.Sleep(50 * time.Millisecond)
	cancel()
	early := waitRank(t, "the cancelled waiter", waiter, 5*time.Second)
	if !errors.Is(early.err, context.Canceled) {
		t.Fatalf("the cancelled waiter returned %v, want context.Canceled", early.err)
	}
	if count := fs.callCount(); count != 1 {
		close(release)
		t.Fatalf("the waiter cancellation started another scan: %d", count)
	}

	close(release)
	got := waitRank(t, "the builder behind the cancelled waiter", builder, 5*time.Second)
	if got.err != nil || len(got.items) != 1 || got.items[0].MessageID != 1 {
		t.Fatalf("the builder got %+v / %v, want the one item", got.items, got.err)
	}
	third := startRank(context.Background(), svc)
	after := waitRank(t, "the reader after the cancelled waiter", third, 5*time.Second)
	if after.err != nil || len(after.items) != 1 {
		t.Fatalf("the later reader got %+v / %v, want the cached item", after.items, after.err)
	}
	if count := fs.callCount(); count != 1 {
		t.Fatalf("one build after a waiter cancellation scanned %d times, want 1", count)
	}
}

// Close is idempotent: a second call returns while the first is still
// waiting, and neither call starts another build.
func TestCloseIsIdempotent(t *testing.T) {
	svc, fs := newBlockingService(t, []store.PriorityRow{coldRow(1)}, clocktest.New(testNow))
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	fs.block(started, release)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	reader := startRank(context.Background(), svc)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the cold build never reached the store")
	}
	first := make(chan struct{})
	go func() {
		svc.Close()
		close(first)
	}()
	second := make(chan struct{})
	go func() {
		svc.Close()
		close(second)
	}()
	select {
	case <-second:
	case <-time.After(5 * time.Second):
		t.Fatal("the second Close did not return")
	}
	got := waitRank(t, "the reader behind Close", reader, 5*time.Second)
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("the reader behind Close returned %v, want context.Canceled", got.err)
	}
	select {
	case <-first:
	case <-time.After(5 * time.Second):
		t.Fatal("the first Close did not return")
	}
	if count := fs.callCount(); count != 1 {
		t.Fatalf("Close allowed %d scans, want the one cancelled build", count)
	}
}

// Close is bounded: a store read that ignores cancellation cannot hang
// shutdown. The wait is reported and the store owner still closes after it.
func TestCloseWaitsForANonCooperativeBuild(t *testing.T) {
	original := closeTimeout
	closeTimeout = 50 * time.Millisecond
	t.Cleanup(func() { closeTimeout = original })

	svc, fs := newBlockingService(t, []store.PriorityRow{coldRow(1)}, clocktest.New(testNow))
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	fs.blockUncooperative(started, release)
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	reader := startRank(context.Background(), svc)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the cold build never reached the store")
	}
	begin := time.Now()
	joined := svc.Close()
	elapsed := time.Since(begin)
	if joined {
		t.Fatal("Close reported a join while the build was still running")
	}
	if elapsed < closeTimeout {
		t.Fatalf("Close returned in %s, before the %s bound", elapsed, closeTimeout)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Close waited %s, want the shortened %s bound", elapsed, closeTimeout)
	}
	select {
	case got := <-reader:
		t.Fatalf("the reader returned before the uncooperative build was released: %+v", got)
	default:
	}
	close(release)
	_ = waitRank(t, "the reader after the uncooperative build was released", reader, 5*time.Second)
}

// Close ends a cold build that is still reading the store, so shutdown can
// close the store without a reader behind it. No later call starts a build.
func TestCloseCancelsAColdBuild(t *testing.T) {
	svc, fs := newBlockingService(t, []store.PriorityRow{coldRow(1)}, clocktest.New(testNow))
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	fs.block(started, release)

	reader := startRank(context.Background(), svc)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the cold build never reached the store")
	}
	closed := make(chan struct{})
	go func() {
		svc.Close()
		close(closed)
	}()
	got := waitRank(t, "the reader behind Close", reader, 5*time.Second)
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("a reader behind Close returned %v, want context.Canceled", got.err)
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return after the build was cancelled")
	}
	select {
	case <-release:
	default:
		close(release)
	}

	after := startRank(context.Background(), svc)
	late := waitRank(t, "the reader after Close", after, 5*time.Second)
	if !errors.Is(late.err, context.Canceled) {
		t.Fatalf("a read after Close returned %v, want context.Canceled", late.err)
	}
	if count := fs.callCount(); count != 1 {
		t.Fatalf("Close allowed %d scans, want the one cancelled build", count)
	}
}
