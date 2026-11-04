//go:build fixture

package evidence

// The evidence trigger guard measured on a deterministic synthetic world
// (no private fixture): the guarded path after a failed index run that
// committed nothing, and the counterfactual it replaces (an evidence run
// over the same 1,000 messages), then the same pair in steady state where
// every row is already stored.

import (
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/index"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/runstate"
)

const fixtureTriggerMessages = 1000

// syntheticMailbox is a deterministic replacement for the private fixture:
// distinct senders, hours and bodies, so every signal sees the same input
// in every repetition.
func syntheticMailbox(n int) []mail.Message {
	msgs := make([]mail.Message, 0, n)
	for i := range n {
		sender := fmt.Sprintf("sender%02d@example.org", i%37)
		body := fmt.Sprintf("Message %d: the budget review is due on %d/%02d; please send the invoice before Friday.",
			i, 1+i%28, 1+i%12)
		msgs = append(msgs, msg(fmt.Sprintf("fx%04d", i), sender, i, body))
	}
	return msgs
}

// TestFixtureEvidenceTriggerCost is the Round 23 A/B: a failed index run
// that committed nothing starts no evidence job and changes no byte; the
// evidence run it used to start writes one row per message and costs the
// measured wall, CPU, heap and database growth. Both phases run on the same
// synthetic world in the same process. Run with -count=3 for medians.
func TestFixtureEvidenceTriggerCost(t *testing.T) {
	msgs := syntheticMailbox(fixtureTriggerMessages)
	settings := map[string]any{"evidence.batch_size": 500, "evidence.workers": 4}
	// What the audit measured on the real store: an unavailable embedder,
	// 8 messages failed, everything else never attempted, 0 embedded and 0
	// unchanged. Committed() is false.
	uncommitted := index.Report{State: runstate.Failed, StoppedBy: index.StoppedUnavailable,
		Failed: 8, NotAttempted: fixtureTriggerMessages - 8}

	t.Run("guarded cold", func(t *testing.T) {
		w := newWorld(t, everything, settings)
		w.write(msgs...)
		m := jobs.NewManager(w.cfg, w.bus, nil)
		t.Cleanup(func() { _ = m.Shutdown(t.Context()) })
		stop, err := w.ev.AfterRuns(w.bus, m)
		if err != nil {
			t.Fatal(err)
		}
		defer stop()
		dbBefore, walBefore := w.storeBytes()
		cpuBefore := cpuMillis()
		start := time.Now()
		events.Publish(w.bus, index.TopicFinished, uncommitted)
		stop() // every handler drained
		wall, cpu := time.Since(start), cpuMillis()-cpuBefore
		walAfter := w.walBytes() // before the checkpoint folds it
		dbAfter, _ := w.storeBytes()
		if got := m.List(); len(got) != 0 {
			t.Fatalf("the failed index run started %d evidence job(s): %+v", len(got), got)
		}
		if rows := w.evidenceRows(); rows != 0 {
			t.Fatalf("the failed index run wrote %d evidence row(s)", rows)
		}
		if runs := w.evidenceRuns(); runs != 0 {
			t.Fatalf("the failed index run persisted %d evidence run(s)", runs)
		}
		if dbAfter != dbBefore || walAfter != walBefore {
			t.Fatalf("the failed index run changed the store: db %d -> %d, wal %d -> %d", dbBefore, dbAfter, walBefore, walAfter)
		}
		t.Logf("ROUND23 guarded messages=%d jobs=0 rows=0 runs=0 wall_us=%d cpu_ms=%d db_delta=%d wal_delta=%d",
			fixtureTriggerMessages, wall.Microseconds(), cpu, dbAfter-dbBefore, walAfter-walBefore)
	})

	t.Run("counterfactual first run", func(t *testing.T) {
		w := newWorld(t, everything, settings)
		w.write(msgs...)
		dbBefore, walBefore := w.storeBytes()
		runtime.GC()
		var memBefore runtime.MemStats
		runtime.ReadMemStats(&memBefore)
		cpuBefore := cpuMillis()
		start := time.Now()
		rep, err := w.ev.Run(t.Context(), nil)
		wall, cpu := time.Since(start), cpuMillis()-cpuBefore
		walAfter := w.walBytes()
		var memAfter runtime.MemStats
		runtime.ReadMemStats(&memAfter)
		dbAfter, _ := w.storeBytes()
		if err != nil {
			t.Fatalf("evidence run: %v (%+v)", err, rep)
		}
		if rep.Messages != fixtureTriggerMessages || rep.Written != fixtureTriggerMessages {
			t.Fatalf("the first run must store one row per message: %+v", rep)
		}
		t.Logf("ROUND23 first messages=%d written=%d computed=%d wall_us=%d context_us=%d values_us=%d cpu_ms=%d heap_before=%d heap_after=%d heap_delta=%d total_alloc_delta=%d db_delta=%d wal_delta=%d",
			rep.Messages, rep.Written, rep.Computed, wall.Microseconds(), rep.ContextTime.Microseconds(),
			rep.ValuesTime.Microseconds(), cpu, memBefore.HeapAlloc, memAfter.HeapAlloc,
			int64(memAfter.HeapAlloc)-int64(memBefore.HeapAlloc), memAfter.TotalAlloc-memBefore.TotalAlloc,
			dbAfter-dbBefore, walAfter-walBefore)
	})

	t.Run("steady state", func(t *testing.T) {
		w := newWorld(t, everything, settings)
		w.write(msgs...)
		if rep, err := w.ev.Run(t.Context(), nil); err != nil || rep.Written != fixtureTriggerMessages {
			t.Fatalf("filling the store: %v %+v", err, rep)
		}
		m := jobs.NewManager(w.cfg, w.bus, nil)
		t.Cleanup(func() { _ = m.Shutdown(t.Context()) })
		stop, err := w.ev.AfterRuns(w.bus, m)
		if err != nil {
			t.Fatal(err)
		}
		defer stop()
		dbBefore, walBefore := w.storeBytes()
		runsBefore := w.evidenceRuns()
		cpuBefore := cpuMillis()
		start := time.Now()
		events.Publish(w.bus, index.TopicFinished, uncommitted)
		stop()
		wall, cpu := time.Since(start), cpuMillis()-cpuBefore
		walAfter := w.walBytes()
		dbAfter, _ := w.storeBytes()
		if got := m.List(); len(got) != 0 {
			t.Fatalf("the failed index run started %d evidence job(s): %+v", len(got), got)
		}
		if runs := w.evidenceRuns(); runs != runsBefore {
			t.Fatalf("the failed index run persisted %d evidence run(s), want %d", runs, runsBefore)
		}
		if dbAfter != dbBefore || walAfter != walBefore {
			t.Fatalf("the failed index run changed the store: db %d -> %d, wal %d -> %d", dbBefore, dbAfter, walBefore, walAfter)
		}
		t.Logf("ROUND23 steady_guard messages=%d jobs=0 runs=%d wall_us=%d cpu_ms=%d db_delta=%d wal_delta=%d",
			fixtureTriggerMessages, runsBefore, wall.Microseconds(), cpu, dbAfter-dbBefore, walAfter-walBefore)

		runtime.GC()
		var memBefore runtime.MemStats
		runtime.ReadMemStats(&memBefore)
		cpuBefore = cpuMillis()
		start = time.Now()
		rep, err := w.ev.Run(t.Context(), nil)
		wall, cpu = time.Since(start), cpuMillis()-cpuBefore
		walAfter = w.walBytes()
		var memAfter runtime.MemStats
		runtime.ReadMemStats(&memAfter)
		dbAfter, _ = w.storeBytes()
		if err != nil {
			t.Fatalf("steady evidence run: %v (%+v)", err, rep)
		}
		if rep.Written != 0 || rep.Messages != fixtureTriggerMessages {
			t.Fatalf("a steady run must write nothing: %+v", rep)
		}
		t.Logf("ROUND23 steady_run messages=%d written=0 computed=%d wall_us=%d context_us=%d values_us=%d cpu_ms=%d heap_before=%d heap_after=%d heap_delta=%d total_alloc_delta=%d db_delta=%d wal_delta=%d",
			rep.Messages, rep.Computed, wall.Microseconds(), rep.ContextTime.Microseconds(),
			rep.ValuesTime.Microseconds(), cpu, memBefore.HeapAlloc, memAfter.HeapAlloc,
			int64(memAfter.HeapAlloc)-int64(memBefore.HeapAlloc), memAfter.TotalAlloc-memBefore.TotalAlloc,
			dbAfter-dbBefore, walAfter-walBefore)
	})
}
