package evidence

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// A cancelled run must abandon the batch it is computing, not finish every
// message first: evidence.batch_size can exceed the whole mailbox. Nothing
// of an abandoned batch is written.
func TestCancelInterruptsAnInFlightBatch(t *testing.T) {
	w := newWorld(t, everything, map[string]any{"evidence.batch_size": 100000, "evidence.workers": 1})
	msgs := make([]mail.Message, 20)
	for i := range msgs {
		msgs[i] = msg(fmt.Sprintf("m%d", i), "ann@example.org", i, "body")
	}
	w.write(msgs...)

	var computed atomic.Int64
	for i := range w.ev.signals {
		if w.ev.signals[i].Name != "link_count" {
			continue
		}
		inner := w.ev.signals[i].Extract
		w.ev.signals[i].Extract = func(in *Input) Value {
			time.Sleep(50 * time.Millisecond)
			computed.Add(1)
			return inner(in)
		}
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan Report, 1)
	go func() {
		rep, _ := w.ev.Run(ctx, nil)
		done <- rep
	}()
	deadline := time.Now().Add(5 * time.Second)
	for computed.Load() < 2 { // 10% of the messages
		if time.Now().After(deadline) {
			t.Fatalf("only %d message(s) computed before the deadline", computed.Load())
		}
		time.Sleep(time.Millisecond)
	}
	started := time.Now()
	cancel()
	var rep Report
	select {
	case rep = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Run returned after %s", elapsed)
	}
	if !rep.Cancelled || rep.State != runstate.Cancelled || rep.Error == "" {
		t.Fatalf("cancelled run = %+v", rep)
	}
	if rep.Written != 0 || rep.Done != 0 {
		t.Fatalf("an abandoned batch was committed: %+v", rep)
	}
	var rows int
	if err := w.st.EachEvidence(t.Context(), func(store.EvidenceRow) error {
		rows++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Fatalf("%d evidence row(s) were committed from the cancelled batch", rows)
	}
}
