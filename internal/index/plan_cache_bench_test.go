package index

import (
	"fmt"
	"testing"
)

// BenchmarkPlanReconcile measures the steady-state automatic reconcile: one
// Ensure pass over a mailbox that the previous pass already covered and no
// write has touched since. "states/op" is the number of full
// EmbeddingStates reads a pass performed (the plan cache's measurement
// point); "scans/op" is the same number under its SQL name. Before the plan
// cache every pass reads the states once.
func BenchmarkPlanReconcile(b *testing.B) {
	for _, n := range []int{500, 5000} {
		b.Run(fmt.Sprintf("messages=%d", n), func(b *testing.B) {
			w := newWorld(b, nil)
			w.mails(n)
			w.indexAll()
			if _, err := w.ix.Ensure(b.Context(), nil); err != nil {
				b.Fatal(err)
			}
			scans := w.ix.planScans.Load()
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := w.ix.Ensure(b.Context(), nil); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			perOp := float64(w.ix.planScans.Load()-scans) / float64(b.N)
			b.ReportMetric(perOp, "states/op")
			b.ReportMetric(perOp, "scans/op")
		})
	}
}
