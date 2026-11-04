package api

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/logx"
)

// sseBenchWriter is the no-network SSE sink: it counts the framed bytes and
// discards them. tailLogs only needs Write and Flush (http.NewResponseController
// treats the missing deadline setter as ErrNotSupported).
type sseBenchWriter struct{ n int }

func (w *sseBenchWriter) Header() http.Header         { return http.Header{} }
func (w *sseBenchWriter) Write(p []byte) (int, error) { w.n += len(p); return len(p), nil }
func (w *sseBenchWriter) WriteHeader(int)             {}
func (w *sseBenchWriter) Flush()                      {}

// BenchmarkSSELogFanout measures the production fan-out unit: publish M log
// entries through a real logger, then let each of N independent cursors read
// the ring once through tailLogs and frame every new entry for its client.
//
// File logging is off so the measurement is the ring/SSE path, not disk. The
// event bus stays on: that is the configuration in which an SSE log consumer
// exists, and it is the condition under which the entry's wire JSON is cached.
// The ring is prefilled through the real logger to the default 2000 retained
// entries before the timer starts.
func BenchmarkSSELogFanout(b *testing.B) {
	for _, clients := range []int{1, 4, 8} {
		for _, entries := range []int{1, 100} {
			b.Run(fmt.Sprintf("clients=%d/entries=%d", clients, entries), func(b *testing.B) {
				benchmarkSSELogFanout(b, clients, entries)
			})
		}
	}
}

func benchmarkSSELogFanout(b *testing.B, clients, entries int) {
	b.Helper()
	bus := events.New()
	b.Cleanup(bus.Close)
	cfg := configtest.NewWithBus(b, bus, map[string]any{
		"log.file.enabled":   false,
		"log.events.enabled": true,
	})
	logs, err := logx.Setup(cfg, bus)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = logs.Close() })

	logger := logx.For("bench.sse")
	for i := range 2000 {
		logger.Info("seed entry", "i", i, "attempt", 1, "mailbox", "bench@example.invalid")
	}

	srv := &Server{
		services: Services{Logs: ringLogs{ring: logs.Ring()}},
		hub:      &Hub{done: make(chan struct{})},
	}
	writers := make([]*sseBenchWriter, clients)
	cursors := make([]uint64, clients)
	_, last := logs.Ring().Bounds()
	for i := range cursors {
		cursors[i] = last
	}
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := range entries {
			logger.Info("fanout entry", "i", j, "attempt", 2, "mailbox", "bench@example.invalid")
		}
		for c := range clients {
			if writers[c] == nil {
				writers[c] = &sseBenchWriter{}
			}
			next, err := srv.tailLogs(ctx, &sseWriter{w: writers[c], timeout: time.Second}, cursors[c])
			if err != nil {
				b.Fatal(err)
			}
			cursors[c] = next
		}
	}
	b.StopTimer()

	var bytes int64
	for _, w := range writers {
		if w != nil {
			bytes += int64(w.n)
		}
	}
	perOp := float64(bytes) / float64(b.N)
	b.ReportMetric(perOp, "bytes/op")
	// One coalesced wake per client and read is the contract: 100 entries
	// must cost well under a kilobyte, not one frame per entry.
	if entries == 100 && clients > 0 && perOp >= 1024 {
		b.Fatalf("wake fanout for %d entries costs %.1f bytes/op (budget 1024)", entries, perOp)
	}
}

// BenchmarkLogsSnapshot measures one /api/logs page against the production
// ring at its largest configured retention. It must allocate the page
// (default 500), not the 100,000 retained entries.
func BenchmarkLogsSnapshot(b *testing.B) {
	bus := events.New()
	b.Cleanup(bus.Close)
	cfg := configtest.NewWithBus(b, bus, map[string]any{
		"log.file.enabled":   false,
		"log.events.enabled": false,
		"log.ring.size":      100000,
	})
	logs, err := logx.Setup(cfg, bus)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = logs.Close() })
	logger := logx.For("bench.logs")
	for i := range 100_000 {
		logger.Info("snapshot entry", "i", i, "attempt", 1)
	}
	logsAPI := ringLogs{ring: logs.Ring()}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		page, err := logsAPI.Snapshot(LogQuery{Limit: 500})
		if err != nil {
			b.Fatal(err)
		}
		if len(page.Entries) != 500 {
			b.Fatalf("page has %d entries", len(page.Entries))
		}
	}
}
