package syncer

import (
	"sort"
	"time"

	"github.com/LLM-4-People/mailsense/internal/runstate"
)

// SourceStatus is the last observed outcome of one source's runs. Fresh is
// the honesty bit: a successful run set it, a later failed or cancelled run
// cleared it, and a run in progress leaves it unchanged until its outcome is
// known. A source that never completed a run is not fresh.
type SourceStatus struct {
	SourceID    string         `json:"source_id"`
	Running     bool           `json:"running"`
	Fresh       bool           `json:"fresh"`
	LastAttempt time.Time      `json:"last_attempt,omitempty"`
	LastSuccess time.Time      `json:"last_success,omitempty"`
	LastState   runstate.State `json:"last_state,omitempty"`
	LastError   string         `json:"last_error,omitempty"`
	Partial     bool           `json:"partial,omitempty"`
}

// Status is the aggregate a read side can trust: every source that has run
// reports its own outcome, and Synced is true only when all of them are
// fresh. Boot starts empty (not synced) until the first run succeeds.
type Status struct {
	Running     bool           `json:"running"`
	Synced      bool           `json:"synced"`
	LastAttempt time.Time      `json:"last_attempt,omitempty"`
	LastSuccess time.Time      `json:"last_success,omitempty"`
	LastState   runstate.State `json:"last_state,omitempty"`
	LastError   string         `json:"last_error,omitempty"`
	Partial     bool           `json:"partial,omitempty"`
	Sources     []SourceStatus `json:"sources"`
}

// Status is the current sync truth. It never treats "no run yet" as synced.
func (s *Syncer) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Status{Sources: make([]SourceStatus, 0, len(s.status))}
	for _, st := range s.status {
		out.Sources = append(out.Sources, st)
	}
	sort.Slice(out.Sources, func(i, j int) bool { return out.Sources[i].SourceID < out.Sources[j].SourceID })
	if len(out.Sources) == 0 {
		return out
	}
	out.Synced = true
	first := true
	for _, st := range out.Sources {
		out.Running = out.Running || st.Running
		out.Synced = out.Synced && st.Fresh
		if first || st.LastAttempt.After(out.LastAttempt) {
			out.LastAttempt = st.LastAttempt
			out.LastState = st.LastState
			out.LastError = st.LastError
			out.Partial = st.Partial
		}
		if !st.LastSuccess.IsZero() && (first || out.LastSuccess.IsZero() || st.LastSuccess.Before(out.LastSuccess)) {
			out.LastSuccess = st.LastSuccess
		}
		first = false
	}
	return out
}

// Busy reports whether any source currently holds its run claim.
func (s *Syncer) Busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.running) > 0
}

func (s *Syncer) noteAttempt(sourceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.status[sourceID]
	st.SourceID = sourceID
	st.Running = true
	st.LastAttempt = s.clock.Now().UTC()
	s.status[sourceID] = st
}

func (s *Syncer) noteFinish(sourceID string, rep Report) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.status[sourceID]
	st.SourceID = sourceID
	st.Running = false
	st.LastState = rep.Status
	st.LastError = rep.Error
	st.Partial = rep.Partial
	switch rep.Status {
	case runstate.Succeeded:
		st.Fresh = true
		st.LastSuccess = st.LastAttempt
	case runstate.Failed, runstate.Cancelled:
		st.Fresh = false
	}
	s.status[sourceID] = st
}
