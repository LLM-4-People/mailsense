//go:build fixture

package store

// Priority-read benchmarks over the prepared fixture store
// (scripts/bench-layers.sh: PERF_STORE_DIR). One iteration scans the whole
// mailbox, so run with -benchtime=1x. The question selection is the
// ranking's own: this is the production read, not the full-read fallback.

import (
	"database/sql"
	"fmt"
	"slices"
	"testing"
	"time"
)

// BenchmarkEachPriorityReplySelected measures the light reply pass with the
// fifteen questions the ranking reads, the decode the priority build does
// for every owner message.
func BenchmarkEachPriorityReplySelected(b *testing.B) {
	s := benchStore(b)
	fingerprint := benchFingerprint(b, s)
	keys := benchQuestionKeys(priorityBenchSelection())
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
				if err := s.EachPriorityReply(b.Context(), fingerprint, tc.preview, keys, func(PriorityRow) error {
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

// BenchmarkEachPriorityReplySerialSelected is the pre-block reference read
// (one row scanned and decoded before the next) over the same fixture and
// selection, so a run of both benchmarks measures the block pipeline against
// the old read under the same load.
func BenchmarkEachPriorityReplySerialSelected(b *testing.B) {
	s := benchStore(b)
	fingerprint := benchFingerprint(b, s)
	keys := benchQuestionKeys(priorityBenchSelection())
	b.ResetTimer()
	for range b.N {
		rows := 0
		if err := serialPriorityRead(b.Context(), b, s, fingerprint, false, false, keys, func(PriorityRow) error {
			rows++
			return nil
		}); err != nil {
			b.Fatal(err)
		}
		if rows == 0 {
			b.Fatal("no rows")
		}
	}
}

// openPriorityRows opens one priority scan without decoding anything, so
// the block-size benchmark can hand the same SQL read to a different block
// bound.
func openPriorityRows(tb testing.TB, s *Store, fingerprint string, includePreview, body bool) *sql.Rows {
	tb.Helper()
	vis, err := s.visible(tb.Context())
	if err != nil {
		tb.Fatal(err)
	}
	rows, err := s.db.QueryContext(tb.Context(), priorityQuery(vis, includePreview, body, true), fingerprint)
	if err != nil {
		tb.Fatal(err)
	}
	return rows
}

// BenchmarkEachPriorityReplyVariants measures the serial read and the block
// read at three block bounds in one iteration, rotating their order. The
// per-iteration serial/block ratio cancels the machine's load drift, so the
// reported median ratio is the fair comparison; the means are diagnostics.
func BenchmarkEachPriorityReplyVariants(b *testing.B) {
	s := benchStore(b)
	fingerprint := benchFingerprint(b, s)
	keys := benchQuestionKeys(priorityBenchSelection())
	sizes := []int{128, 256, 512}
	serial := func() time.Duration {
		start := time.Now()
		if err := serialPriorityRead(b.Context(), b, s, fingerprint, false, false, keys, func(PriorityRow) error { return nil }); err != nil {
			b.Fatal(err)
		}
		return time.Since(start)
	}
	block := func(size int) time.Duration {
		rows := openPriorityRows(b, s, fingerprint, false, false)
		defer rows.Close()
		start := time.Now()
		if err := eachPriorityBlockOf(b.Context(), rows, priorityQuestionSelection(keys), false, true, size, func(PriorityRow) error { return nil }); err != nil {
			b.Fatal(err)
		}
		return time.Since(start)
	}
	var serialTotal time.Duration
	totals := make(map[int]time.Duration, len(sizes))
	ratios := make(map[int][]float64, len(sizes))
	for i := range b.N {
		// Four scans per iteration, rotated so every variant is first a
		// quarter of the time (the first scan pays the cold page cache).
		variants := []int{-1, sizes[0], sizes[1], sizes[2]}
		order := make([]int, len(variants))
		for k := range variants {
			order[k] = variants[(k+i)%len(variants)]
		}
		var serialTime time.Duration
		blockTimes := make(map[int]time.Duration, len(sizes))
		for _, size := range order {
			if size < 0 {
				serialTime = serial()
				continue
			}
			blockTimes[size] = block(size)
		}
		serialTotal += serialTime
		for _, size := range sizes {
			totals[size] += blockTimes[size]
			ratios[size] = append(ratios[size], float64(serialTime)/float64(blockTimes[size]))
		}
	}
	mean := func(d time.Duration) float64 { return float64(d.Nanoseconds()) / float64(b.N) }
	b.ReportMetric(mean(serialTotal), "serial-ns/op")
	for _, size := range sizes {
		b.ReportMetric(mean(totals[size]), fmt.Sprintf("block%d-ns/op", size))
		b.ReportMetric(median(ratios[size]), fmt.Sprintf("median-ratio-%d", size))
	}
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	slices.Sort(sorted)
	if len(sorted)%2 == 1 {
		return sorted[len(sorted)/2]
	}
	return (sorted[len(sorted)/2-1] + sorted[len(sorted)/2]) / 2
}

// BenchmarkEachPriorityMessageSelected measures the same production
// selection through the body-carrying read.
func BenchmarkEachPriorityMessageSelected(b *testing.B) {
	s := benchStore(b)
	fingerprint := benchFingerprint(b, s)
	keys := benchQuestionKeys(priorityBenchSelection())
	b.ResetTimer()
	for range b.N {
		rows := 0
		if err := s.EachPriorityMessage(b.Context(), fingerprint, false, keys, func(PriorityRow) error {
			rows++
			return nil
		}); err != nil {
			b.Fatal(err)
		}
		if rows == 0 {
			b.Fatal("no rows")
		}
	}
}
