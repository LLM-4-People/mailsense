package index

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/LLM-4-People/mailsense/internal/learning"
)

// Blocker codes of the embeddings stage.
const (
	BlockNoEmbedder  = "no_embedder"
	BlockQuarantined = "embedding_quarantined"
)

// Stage is the learning stage of the semantic index: a vector of the
// current space for every eligible message of the picked stores. A missing
// embedder (no runtime build for this machine, a provider down) is not a
// startup error — search runs full-text only and says so — but learning
// cannot finish without it, so it is a blocker with its remedy.
func (ix *Indexer) Stage() learning.Stage { return stage{ix: ix} }

type stage struct{ ix *Indexer }

// noEmbedderBlocker is the one "blocked by the embedder" answer of the stage:
// a failed loader and a stored unavailable run name the same code and the same
// remedy, so the learning screen offers one action.
func noEmbedderBlocker(detail string) learning.Blocker {
	return learning.Blocker{Code: BlockNoEmbedder, Detail: detail,
		Actions: []learning.Action{learning.Do(learning.ActionChooseEmbedder)}}
}

func (stage) ID() string { return learning.StageEmbeddings }

func (s stage) Status(ctx context.Context) (learning.StageStatus, error) {
	out := learning.StageStatus{State: learning.Running, Progress: learning.Progress{Unit: learning.UnitMessages}}
	e, err := s.ix.src.Embedder()
	if err != nil {
		out.State = learning.Blocked
		out.Blockers = []learning.Blocker{noEmbedderBlocker(err.Error())}
		return out, nil
	}
	// A run in progress answers from its live state: a progress refresh
	// must not plan the whole 35k-message coverage again. Total is the work
	// selected by this run; before one is selected (0), the eligible count
	// is the honest denominator. Completion and cancellation fall through
	// to the full coverage below, which is cached while its inputs are
	// unchanged: a coalesced status refresh must not rescan the mailbox.
	if live, ok := s.ix.liveFor(e.Space()); ok {
		total := int64(live.total)
		if total == 0 {
			total = int64(live.eligible)
		}
		out.State = learning.Running
		out.Progress = learning.Progress{Done: int64(live.done), Total: total, Unit: learning.UnitMessages,
			Embedded: int64(live.embedded), Unchanged: int64(live.unchanged), Failed: int64(live.failed)}
		return out, nil
	}
	cov, err := s.ix.coverage(ctx, e.Space())
	if err != nil {
		return out, err
	}
	out.Progress.Done, out.Progress.Total = int64(cov.Indexed), int64(cov.Eligible)
	unavailable, err := s.lastRunUnavailable(ctx)
	if err != nil {
		return out, err
	}
	switch {
	case unavailable != "" && cov.Indexed < cov.Eligible:
		out.State = learning.Blocked
		out.Blockers = []learning.Blocker{noEmbedderBlocker(unavailable)}
	case cov.Quarantined > 0:
		out.State = learning.Blocked
		out.Blockers = []learning.Blocker{{Code: BlockQuarantined, Actions: []learning.Action{learning.Do(learning.ActionReviewFailures, "log", "index",
			"messages", strconv.Itoa(cov.Quarantined), "attempts", strconv.Itoa(s.ix.cfg.Int(SettingQuarantineAfter)))}}}
	case cov.Eligible == 0:
		out.State, out.Note = learning.Pending, "no eligible mail yet"
	case cov.Searchable()+cov.Partial >= cov.Eligible:
		// Every eligible message either has a vector of the space or waits
		// for details the source could not read: a run over the current
		// inputs can do no more. A stale vector (the message changed) is
		// still searchable. Both are warnings, not a reason to keep the
		// whole app in "learning" forever.
		out.State = learning.Done
		switch {
		case cov.Partial > 0:
			out.Note = fmt.Sprintf("%d message(s) waiting for details: their vector is partial", cov.Partial)
		case cov.Stale > 0:
			out.Note = fmt.Sprintf("%d message(s) changed: their vector is being refreshed", cov.Stale)
		}
	}
	return out, nil
}

// lastRunEntry is the unavailable detail of the newest index run and the
// run sequence it was read at.
type lastRunEntry struct {
	seq         uint64
	unavailable string
}

// lastRunUnavailable is the error of the last index run when it stopped
// because the embedder could not serve ("" otherwise), reused while no run
// has finished since. The run sequence is read before the store read: a
// run finishing during the read bumps it, so the old answer is cached under
// the old sequence and the next status read recomputes.
func (s stage) lastRunUnavailable(ctx context.Context) (string, error) {
	seq := s.ix.runSeq.Load()
	s.ix.runMu.Lock()
	entry := s.ix.lastRun
	s.ix.runMu.Unlock()
	if entry != nil && entry.seq == seq {
		return entry.unavailable, nil
	}
	unavailable, err := s.ix.readLastRunUnavailable(ctx)
	if err != nil {
		return "", err
	}
	s.ix.runMu.Lock()
	s.ix.lastRun = &lastRunEntry{seq: seq, unavailable: unavailable}
	s.ix.runMu.Unlock()
	return unavailable, nil
}

// readLastRunUnavailable is the uncached newest-run read.
func (ix *Indexer) readLastRunUnavailable(ctx context.Context) (string, error) {
	runs, err := ix.st.ListIndexRuns(ctx, 1)
	if err != nil || len(runs) == 0 {
		return "", err
	}
	var rep Report
	if err := json.Unmarshal([]byte(runs[0].Report), &rep); err != nil || rep.StoppedBy != StoppedUnavailable {
		return "", nil
	}
	return rep.Error, nil
}
