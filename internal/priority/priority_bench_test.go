//go:build fixture

package priority

// The priority read on the prepared fixture store (scripts/bench-layers.sh:
// PERF_STORE_DIR), with the production role-key selection. Two preview modes:
// false is the normal pass, true is the live learning.test_mode pass. One
// iteration scans the whole routed mailbox, so run with -benchtime=1x.

import (
	"os"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/layer1"
	"github.com/LLM-4-People/mailsense/internal/learning/learningtest"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

func BenchmarkEachPriorityReply(b *testing.B) {
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
	profile, err := taxonomy.Load(cfg)
	if err != nil {
		b.Fatal(err)
	}
	keys := neededQuestions(profile)
	if len(keys) == 0 {
		b.Fatal("the profile has no ranking questions")
	}
	for _, tc := range []struct {
		name    string
		preview bool
	}{
		{"preview=false", false},
		{"preview=true", true},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ResetTimer()
			for range b.N {
				rows := 0
				if err := st.EachPriorityReply(b.Context(), routeKey, tc.preview, keys, func(store.PriorityRow) error {
					rows++
					return nil
				}); err != nil {
					b.Fatal(err)
				}
				if rows == 0 {
					b.Fatal("no rows")
				}
			}
		})
	}
}

// BenchmarkBuildRankingPhases reports the live mailbox build's wall time by
// phase on the prepared fixture (scripts/bench-*.sh set PERF_STORE_DIR). The
// scan and the thread resolution are the two store calls the build makes;
// scoring is the pure pass between them. Run with -benchtime=3x and read the
// per-phase metrics, not ns/op.
func BenchmarkBuildRankingPhases(b *testing.B) {
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
	profile, err := taxonomy.Load(cfg)
	if err != nil {
		b.Fatal(err)
	}
	svc, err := New(cfg, Deps{Store: st, Threads: st, Gate: learningtest.Open{}, Config: cfg,
		RouteKey: func() (string, error) { return routeKey, nil }})
	if err != nil {
		b.Fatal(err)
	}
	rows, conversations, err := svc.replySignals(b.Context(), profile, routeKey, false)
	if err != nil {
		b.Fatal(err)
	}
	ids := make([]string, 0, len(conversations))
	for id := range conversations {
		ids = append(ids, id)
	}
	signals := threadSignalsFor(rows)
	states, err := st.ThreadStates(b.Context(), ids, signals)
	if err != nil {
		b.Fatal(err)
	}
	b.Logf("mailbox: %d owner rows, %d conversations", rows.len(), len(ids))

	b.Run("scan", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			scanned, convs, err := svc.replySignals(b.Context(), profile, routeKey, false)
			if err != nil {
				b.Fatal(err)
			}
			if scanned.len() != rows.len() || len(convs) != len(ids) {
				b.Fatalf("scanned %d rows/%d conversations", scanned.len(), len(convs))
			}
		}
	})
	b.Run("threads", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if _, err := st.ThreadStates(b.Context(), ids, signals); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("score", func(b *testing.B) {
		now := svc.clock.Now()
		b.ReportAllocs()
		for range b.N {
			scored := 0
			rows.each(func(row *rankedRow) {
				item, ok := scoreRowSignals(rowInput(row, now, states[row.conversationID]),
					row.signals, row.received, row.dated, false)
				_ = item
				if ok {
					scored++
				}
			})
			if scored == 0 {
				b.Fatal("no row entered the ranking")
			}
		}
	})
}
