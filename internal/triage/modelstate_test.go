package triage

import (
	"encoding/json"
	"strings"
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

// The judge state is refused for a message outside the triaged owner-scope
// population, exactly like the reading path's population.
func TestModelStateRefusesAnIneligibleMessage(t *testing.T) {
	w := newWorld(t, nil)
	w.mail("a@client.com", "A", "S", 0, 0)
	if _, err := w.tr.ModelState(t.Context(), 987654); err == nil {
		t.Fatal("an unknown row got a state")
	}
}
