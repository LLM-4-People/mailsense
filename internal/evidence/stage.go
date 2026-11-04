package evidence

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// Blocker codes of the evidence stage.
const BlockRunFailed = "evidence_run_failed"

// Stage is the learning stage "evidence": done when the last succeeded
// run read the current inputs (every visible message, its verdict and
// vector, the capabilities, the schema with its settings and data) and no
// message still waits for its details — so every chosen message has every
// signal its source can give; running otherwise (the next run follows a
// sync, an index run or a settings change); blocked when the last run over
// the current inputs failed.
func (e *Engine) Stage() learning.Stage { return e.stage }

type stage struct {
	e  *Engine
	mu sync.Mutex
	// the inputs fingerprint and counts, kept while nothing they read
	// changed (the store's generation and vector sequence, the schema,
	// the space, the evidence writes): a status call is cheap
	key      string
	inputs   string
	counts   store.EvidenceCounts
	computed int // times the fingerprint was computed (observability, tests)
}

func (*stage) ID() string { return learning.StageEvidence }

func (s *stage) Status(ctx context.Context) (learning.StageStatus, error) {
	out := learning.StageStatus{State: learning.Running, Progress: learning.Progress{Unit: learning.UnitMessages}}
	if _, err := clock.Zone(s.e.cfg.String(clock.SettingZone)); errors.Is(err, clock.ErrZoneUnconfirmed) {
		out.State, out.Note = learning.Pending, "waits for the confirmed time zone (machine stage): dates are resolved in the owner's day"
		return out, nil
	}
	inputs, c, err := s.current(ctx)
	if err != nil {
		return out, err
	}
	out.Progress.Done, out.Progress.Total = int64(c.Done), int64(c.Total)
	switch {
	case c.Total == 0:
		out.State, out.Note = learning.Pending, "no mail yet"
		return out, nil
	case c.Pending > 0:
		// A message whose details the source could not read stays partial
		// until the owner retries; waiting for it forever would keep the
		// whole app in "learning". The run over the current inputs already
		// did everything it can, so fall through to its state and report
		// the partial signals as a warning.
		out.Note = fmt.Sprintf("%d message(s) waiting for details: their signals are partial", c.Pending)
	}
	runs, err := s.e.st.ListEvidenceRuns(ctx, 1)
	if err != nil || len(runs) == 0 || runs[0].Inputs != inputs {
		return out, err
	}
	switch runs[0].State {
	case runstate.Succeeded:
		out.State = learning.Done
	case runstate.Failed:
		out.State = learning.Blocked
		out.Blockers = []learning.Blocker{{Code: BlockRunFailed, Detail: runs[0].Error,
			Actions: []learning.Action{learning.Do(learning.ActionReviewFailures, "log", "evidence"), learning.Do(learning.ActionRunEvidence)}}}
	}
	return out, nil
}

// current is the inputs fingerprint and the counts, computed again only
// when what they read changed.
func (s *stage) current(ctx context.Context) (string, store.EvidenceCounts, error) {
	sch, err := s.e.schema()
	if err != nil {
		return "", store.EvidenceCounts{}, err
	}
	seq, err := s.e.st.EmbeddingSequence(ctx)
	if err != nil {
		return "", store.EvidenceCounts{}, err
	}
	space := s.e.spaceName()
	key := fmt.Sprintf("%d|%d|%s|%s|%s", s.e.st.MailGeneration(), seq, s.e.Version(), space, sch.JSON)
	s.mu.Lock()
	defer s.mu.Unlock()
	if key == s.key {
		return s.inputs, s.counts, nil
	}
	id, err := s.e.st.LookupEvidenceSchema(ctx, sch.JSON)
	if err != nil {
		return "", store.EvidenceCounts{}, err
	}
	c, err := s.e.st.EvidenceProgress(ctx, id)
	if err != nil {
		return "", store.EvidenceCounts{}, err
	}
	inputs, err := s.e.inputs(ctx, sch, space)
	if err != nil {
		return "", store.EvidenceCounts{}, err
	}
	s.key, s.inputs, s.counts = key, inputs, c
	s.computed++
	return inputs, c, nil
}
