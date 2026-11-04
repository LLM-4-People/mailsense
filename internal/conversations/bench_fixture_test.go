//go:build fixture

package conversations

// Benchmarks over a prepared fixture store (PERF_STORE_DIR). The service
// receives the priority truth as an in-memory map, so the measurement covers
// the snapshot and the seven warm pages, not the priority build.

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
)

type benchPriority struct {
	states map[string]threadintel.Assessment
}

func (p benchPriority) ThreadStates(context.Context) (map[string]threadintel.Assessment, error) {
	return p.states, nil
}

// blockingPriority holds one snapshot refresh until the benchmark releases it,
// so the stale generation of a write stays installed for the whole loop.
type blockingPriority struct {
	states  map[string]threadintel.Assessment
	block   atomic.Bool
	release chan struct{}
	calls   atomic.Int64
}

func (p *blockingPriority) ThreadStates(ctx context.Context) (map[string]threadintel.Assessment, error) {
	p.calls.Add(1)
	if p.block.Load() {
		select {
		case <-p.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return p.states, nil
}

func benchStore(b testing.TB) *store.Store {
	b.Helper()
	dir := os.Getenv("PERF_STORE_DIR")
	if dir == "" {
		b.Skip("PERF_STORE_DIR is not set (scripts/bench-startup.sh sets it)")
	}
	cfg, err := config.Load(config.Options{Paths: config.Paths{DataDir: dir}})
	if err != nil {
		b.Fatal(err)
	}
	st, err := store.Open(b.Context(), cfg, nil, nil)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = st.Close() })
	return st
}

func benchStates(b testing.TB, st *store.Store) map[string]threadintel.Assessment {
	b.Helper()
	set, err := st.ConversationDigests(b.Context())
	if err != nil {
		b.Fatal(err)
	}
	states := make(map[string]threadintel.Assessment, len(set.Conversations))
	for _, d := range set.Conversations {
		states[d.ConversationID] = threadintel.Assessment{
			ConversationID: d.ConversationID,
			State:          threadintel.AwaitingOwner,
			Reason:         threadintel.ReasonInboundLast,
			Visibility:     threadintel.Complete,
		}
	}
	return states
}

func benchService(b testing.TB) *Service {
	b.Helper()
	st := benchStore(b)
	svc, err := New(Deps{Store: st, Priority: benchPriority{states: benchStates(b, st)}})
	if err != nil {
		b.Fatal(err)
	}
	return svc
}

// BenchmarkConversationsWarm7 measures the start-up warm: the default page
// and every state chip through the page cache.
func BenchmarkConversationsWarm7(b *testing.B) {
	svc := benchService(b)
	b.ResetTimer()
	for range b.N {
		if _, err := svc.Page(b.Context(), Query{Limit: DefaultLimit}); err != nil {
			b.Fatal(err)
		}
		for _, state := range threadintel.States {
			if _, err := svc.Page(b.Context(), Query{Limit: DefaultLimit, State: state}); err != nil {
				b.Fatal(err)
			}
		}
	}
}

// BenchmarkConversationsPageState measures one uncached chip build over a
// warm snapshot, the work the snapshot projection removes six times over.
func BenchmarkConversationsPageState(b *testing.B) {
	svc := benchService(b)
	if _, err := svc.Page(b.Context(), Query{Limit: DefaultLimit}); err != nil {
		b.Fatal(err)
	}
	q, err := normalizeQuery(Query{Limit: DefaultLimit, State: threadintel.AwaitingOwner})
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for range b.N {
		if _, err := svc.buildPage(b.Context(), q, false); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPageWarmHit measures the request a repeated identical click makes
// once the snapshot and its page are both installed.
func BenchmarkPageWarmHit(b *testing.B) {
	svc := benchService(b)
	if _, err := svc.Page(b.Context(), Query{Limit: 50}); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := svc.Page(b.Context(), Query{Limit: 50}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPageBuildWarmSnapshot measures one uncached page build (cursor,
// state selection, enrichment) over a warm snapshot, the foreground work a
// page miss pays.
func BenchmarkPageBuildWarmSnapshot(b *testing.B) {
	svc := benchService(b)
	if _, err := svc.Page(b.Context(), Query{Limit: 50}); err != nil {
		b.Fatal(err)
	}
	q, err := normalizeQuery(Query{Limit: 50})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := svc.buildPage(b.Context(), q, false); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPageGenerationMove measures the repeated click while one mail
// write has moved the store generation and the snapshot refresh is still in
// flight. A generation-keyed page cache misses on every call here; a page
// keyed by the snapshot it was built from keeps serving the warmed page and
// rebuilds only once, after the refresh lands.
func BenchmarkPageGenerationMove(b *testing.B) {
	st := benchStore(b)
	priority := &blockingPriority{states: benchStates(b, st), release: make(chan struct{})}
	svc, err := New(Deps{Store: st, Priority: priority})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { close(priority.release) })
	if _, err := svc.Page(b.Context(), Query{Limit: 50}); err != nil {
		b.Fatal(err)
	}
	priority.block.Store(true)

	var rowID int64
	if err := st.EachMessage(b.Context(), store.MessageFilter{}, func(r *store.MessageRow) error {
		if rowID == 0 {
			rowID = r.RowID
		}
		return nil
	}); err != nil {
		b.Fatal(err)
	}
	if err := st.WriteVerdicts(b.Context(), "bench", []store.VerdictRow{{
		RowID: rowID, Verdict: scope.Owner, Reason: scope.ReasonRecipientMatch,
	}}); err != nil {
		b.Fatal(err)
	}
	// One request starts the detached snapshot refresh; it blocks in the
	// priority resolver until cleanup, so the snapshot stays stale.
	if _, err := svc.Page(b.Context(), Query{Limit: 50}); err != nil {
		b.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for priority.calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if priority.calls.Load() < 2 {
		b.Fatal("the snapshot refresh never reached the priority resolver")
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := svc.Page(b.Context(), Query{Limit: 50}); err != nil {
			b.Fatal(err)
		}
	}
}
