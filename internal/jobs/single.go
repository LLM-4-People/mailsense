package jobs

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/LLM-4-People/mailsense/internal/logx"
)

// Single is the one-at-a-time guard of one kind of run (a background job
// or a synchronous run) that never loses a request: a run wanted while
// another is in progress starts once that one ends, and every request
// made meanwhile is served by that one run (they coalesce).
type Single struct {
	mu      sync.Mutex
	busy    bool
	pending func() // the run to start when the current one ends (nil: none wanted)
}

// Begin claims the slot for a run; false while one is in progress.
func (s *Single) Begin() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy {
		return false
	}
	s.busy = true
	return true
}

// End releases the slot of the run that claimed it. A run wanted
// meanwhile takes the slot over and starts now.
func (s *Single) End() {
	s.mu.Lock()
	next := s.pending
	s.pending = nil
	s.busy = next != nil
	s.mu.Unlock()
	if next != nil {
		s.launch(next)
	}
}

// Want asks for a run: start runs now with the slot claimed when no run
// is in progress, else once the current one ends (the latest start of the
// requests made meanwhile). start owns the slot: it must call End when
// its run ends, or at once when the run cannot start.
func (s *Single) Want(start func()) {
	s.mu.Lock()
	if s.busy {
		s.pending = start
		s.mu.Unlock()
		return
	}
	s.busy = true
	s.mu.Unlock()
	s.launch(start)
}

// launch runs a start closure that owns the slot, recovering a panic so a
// broken start can never leave busy set with no run: the slot is released
// and a request made meanwhile still starts. A panic value can carry mail
// content, so only its type and stack reach the log.
func (s *Single) launch(start func()) {
	err := Safe(func() error {
		start()
		return nil
	})
	var p *PanicError
	if !errors.As(err, &p) {
		return
	}
	logx.For("jobs").Error("single run panicked", "panic_type", fmt.Sprintf("%T", p.Value), "stack", p.Stack)
	s.End()
}

// Busy reports whether a run is in progress.
func (s *Single) Busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy
}

// Pending reports whether a run is wanted after the current one.
func (s *Single) Pending() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pending != nil
}

// StartClaimed starts a job in a slot the caller has already claimed with
// Begin. A manager that refuses the job releases the slot at once; otherwise
// the manager releases it when the job finishes, including when a deadline
// finalizes a body that ignores its context. Evidence and the semantic index
// repeated this shell verbatim.
func StartClaimed(s *Single, m *Manager, kind, name string, run func(context.Context, func(Progress)) error) (*Job, error) {
	j, err := m.startClaimed(s, kind, name, run)
	if err != nil {
		s.End()
	}
	return j, err
}
