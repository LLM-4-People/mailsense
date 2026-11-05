package triage

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/feedback"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// The judge state is the label-free model view: the same message's state
// must be byte-identical before and after labels, reply examples and
// stored predictions change, and it must contain none of them.
func TestModelStateCarriesNoLabelsHistoryOrPredictions(t *testing.T) {
	w := newWorld(t, nil)
	sender := "sender@client.com"
	// The messages are dated after the store clock so the labels written
	// now are causal examples for the judged message (created before it).
	future := base.Add(400 * 24 * time.Hour)
	reply := w.message("reply-0", mail.RoleSent, "Sent", me, "A", "RE: UNIQUE-HISTORY-SUBJECT", future.Add(-time.Hour), sender)
	reply.Body = "UNIQUE-REPLY-BODY"
	w.write(reply)
	other := w.mail(sender, "B", "UNIQUE-EXAMPLE-SUBJECT", future.Sub(base)-30*time.Minute, 0)
	id := w.mail(sender, "A", "The judged message", future.Sub(base), 0)
	state := func() string {
		t.Helper()
		s, err := w.tr.ModelState(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	before := state()
	full := w.states()[id]
	if !strings.Contains(full, "How you handled this sender before") {
		t.Fatalf("the full state lacks the history this test needs:\n%s", full)
	}
	// Adding labels changes the full state (examples) but never the model view.
	p, err := taxonomy.Load(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	lb := feedback.NewLabeler(w.st, nil)
	value, _ := json.Marshal("work")
	if _, err := lb.PutMessage(t.Context(), p, other, map[string]json.RawMessage{"category": value}); err != nil {
		t.Fatal(err)
	}
	if _, err := lb.PutMessage(t.Context(), p, id, map[string]json.RawMessage{"category": value}); err != nil {
		t.Fatal(err)
	}
	after := state()
	if after != before {
		t.Fatalf("labels changed the judge state:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if strings.Contains(after, "UNIQUE-EXAMPLE-SUBJECT") || strings.Contains(after, "UNIQUE-REPLY-BODY") || strings.Contains(after, "UNIQUE-HISTORY-SUBJECT") {
		t.Fatalf("the judge state carries labels or history:\n%s", after)
	}
	again := w.states()[id]
	if !strings.Contains(again, "UNIQUE-EXAMPLE-SUBJECT") || again == full {
		t.Fatalf("the full state did not gain the label example: the comparison proves nothing:\n%s", again)
	}
	// A stored prediction never changes the state either.
	plan, err := w.tr.Plan(t.Context(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	answers, _ := json.Marshal(map[string]engine.Answer{"category": {Type: engine.Choice, Choice: "work",
		Probabilities: map[string]float64{"work": 1}}})
	if err := w.st.SaveDecision(t.Context(), plan.Key, store.Decision{MessageID: id, Revision: 1, StateHash: "x", Answers: string(answers)}); err != nil {
		t.Fatal(err)
	}
	if afterPredictions := state(); afterPredictions != before {
		t.Fatalf("a prediction changed the judge state:\nbefore:\n%s\nafter:\n%s", before, afterPredictions)
	}
}

// The cache path and the judge's state share ONE mail-derived core per mail
// stamp: warming one and then the other pays a single heavy build (the build
// counter and the evidence load, which runs inside buildMailCore after
// threads.Load, prove it), and both label overlays point at the same
// analysis, behaviour and history. A label write reloads the cache's overlay
// only; new mail rebuilds the one core once for both.
func TestCacheAndModelShareOneMailCore(t *testing.T) {
	for _, tc := range []struct {
		name       string
		modelFirst bool
	}{
		{name: "cache first"},
		{name: "model first", modelFirst: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, nil)
			id := w.mail("a@x.org", "A", "Hi", 0, 0)
			ev := &countingEvidence{}
			w.tr.UseEvidence(ev)
			if err := w.cfg.Set("triage.facts.evidence", true); err != nil {
				t.Fatal(err)
			}
			var builds atomic.Int64
			w.tr.build = func(ctx context.Context) (*core, error) {
				builds.Add(1)
				return w.tr.buildMailCore(ctx)
			}
			warmCache := func() *core {
				t.Helper()
				c, err := w.tr.core(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				return c
			}
			warmModel := func() *core {
				t.Helper()
				if _, err := w.tr.ModelState(t.Context(), id); err != nil {
					t.Fatal(err)
				}
				c, err := w.tr.modelCore(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				return c
			}

			var cache, model *core
			if tc.modelFirst {
				model, cache = warmModel(), warmCache()
			} else {
				cache, model = warmCache(), warmModel()
			}
			if got := builds.Load(); got != 1 {
				t.Fatalf("warming the cache and the model built the mail core %d times, want 1", got)
			}
			if got := ev.facts; got != 1 {
				t.Fatalf("warming the cache and the model loaded the evidence facts %d times, want 1 (one threads.Load)", got)
			}
			if model.analysis != cache.analysis || model.history != cache.history ||
				model.behaviour != cache.behaviour ||
				reflect.ValueOf(model.facts).Pointer() != reflect.ValueOf(cache.facts).Pointer() {
				t.Fatal("the cache and the model do not share the mail-derived core")
			}

			// A label write reloads the cache's label overlay: the mail core
			// and the judge's overlay stay put.
			if err := w.st.PutLabel(t.Context(), id, "reply_needed", "1", store.LabelManual); err != nil {
				t.Fatal(err)
			}
			labeled, err := w.tr.core(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if labeled.analysis != cache.analysis || len(labeled.labels) != 1 {
				t.Fatal("a label write did not reload only the label overlay")
			}
			if again, err := w.tr.modelCore(t.Context()); err != nil || again != model {
				t.Fatal("a label write rebuilt the model's overlay")
			}
			if got := builds.Load(); got != 1 {
				t.Fatalf("a label write built the mail core %d times, want 1", got)
			}

			// New mail rebuilds the one mail core once: the first consumer
			// pays it, the second reuses it.
			w.mail("b@x.org", "B", "Hi", time.Hour, 0)
			grown, err := w.tr.core(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if grown.analysis == labeled.analysis {
				t.Fatal("new mail did not rebuild the mail core")
			}
			if got := builds.Load(); got != 2 {
				t.Fatalf("new mail built the mail core %d times, want 2 total", got)
			}
			next, err := w.tr.modelCore(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if next.analysis != grown.analysis {
				t.Fatal("the model did not reuse the rebuilt mail core")
			}
			if got := builds.Load(); got != 2 {
				t.Fatalf("the model built the mail core %d times after the cache, want 2 total", got)
			}
			if got := ev.facts; got != 2 {
				t.Fatalf("the evidence facts were loaded %d times, want 2 (one per mail stamp)", got)
			}
		})
	}
}

// The judge state is refused for a message outside the triaged owner-scope
// population, exactly like the reading path's population.
func TestModelStateRefusesAnIneligibleMessage(t *testing.T) {
	w := newWorld(t, nil)
	w.mail("a@client.com", "A", "S", 0, 0)
	if _, err := w.tr.ModelState(t.Context(), 987654); err == nil {
		t.Fatal("an unknown row got a state")
	}
}
