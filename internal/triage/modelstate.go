package triage

import (
	"context"
	"fmt"

	"github.com/LLM-4-People/mailsense/internal/store"
)

// ModelState is the label-free model view of one eligible message: the
// header, the observed facts and the body, without history, label
// examples, predictions or labels (modelView). It is the judge's state
// and never calls Read, so it is safe before the learning gate opens
// (judging is a learning run, like a triage run). A message outside the
// triaged owner-scope population is refused.
func (t *Triager) ModelState(ctx context.Context, rowID int64) (string, error) {
	c, err := t.modelCore(ctx)
	if err != nil {
		return "", err
	}
	view := modelView(c.composer)
	var state string
	found := false
	err = c.eachEligible(ctx, t.st, nil, []int64{rowID}, func(r *store.MessageRow) error {
		found = true
		state = view.State(r.RowID, &r.Message)
		return nil
	})
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("triage: message %d is not owner mail of a picked store and triaged folder role", rowID)
	}
	return state, nil
}

// modelCore is the core of ModelState, reused until the mail, the profile,
// the rules, the evidence or the selected stores change. Its key leaves
// out the store generation (which label writes bump): every label the
// core was built with is invisible to modelView, so a judge run's own
// writes neither rebuild this cache nor change a state.
func (t *Triager) modelCore(ctx context.Context) (*core, error) {
	key := t.stamp(t.st.MailGeneration())
	t.modelMu.Lock()
	defer t.modelMu.Unlock()
	if t.model != nil && t.modelKey == key {
		return t.model, nil
	}
	c, err := t.buildCore(ctx)
	if err != nil {
		return nil, err
	}
	t.model, t.modelKey = c, key
	return c, nil
}
