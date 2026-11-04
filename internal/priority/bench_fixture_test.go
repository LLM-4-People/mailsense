//go:build fixture

package priority

// The full ranking build on the prepared fixture store (scripts/bench-layers.sh:
// PERF_STORE_DIR). One iteration scans the whole mailbox twice and resolves
// every thread state, so run with -benchtime=1x.

import (
	"os"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/layer1"
	"github.com/LLM-4-People/mailsense/internal/learning/learningtest"
	"github.com/LLM-4-People/mailsense/internal/store"
)

func BenchmarkBuildRankingFixture(b *testing.B) {
	dir := os.Getenv("PERF_STORE_DIR")
	if dir == "" {
		b.Skip("PERF_STORE_DIR is not set (scripts/bench-layers.sh sets it)")
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
	routeKey, err := layer1.RouteKey(cfg)
	if err != nil {
		b.Fatal(err)
	}
	svc, err := New(cfg, Deps{Store: st, Threads: st, Gate: learningtest.Open{}, Config: cfg,
		RouteKey: func() (string, error) { return routeKey, nil }})
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for range b.N {
		c, err := svc.buildRanking(b.Context(), false, svc.clock.Now(), routeKey)
		if err != nil {
			b.Fatal(err)
		}
		if len(c.items) == 0 {
			b.Fatal("no ranked items")
		}
	}
}
