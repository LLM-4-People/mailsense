package store

import (
	"context"
	"sync"
)

// Generation counts the writes that change what derived views read
// (messages, folders, the mailbox selection, scope, labels) in this
// process, so a consumer can reuse a derived view while it is unchanged.
// Decisions and runs do not count: views read them fresh. It applies a
// changed selection first (applySelection), so a view cached under the
// old selection is never reused.
func (s *Store) Generation() uint64 {
	s.ensureSelection()
	return s.gen.Load()
}

// MailGeneration is Generation without the label writes: a view of the
// mail itself (threads, behaviour) survives a label click.
func (s *Store) MailGeneration() uint64 {
	s.ensureSelection()
	return s.mailGen.Load()
}

// LabelGeneration counts label writes and every change to the visible
// population (selection, scope verdicts, removals). A view of the labels
// (the hub totals and recent answers) survives an ordinary mail sync, while
// a selection or scope change that can move a label out of view rebuilds it.
// Like Generation and MailGeneration, it applies a pending selection first:
// a cache key read before that write would invalidate the value it just
// built.
func (s *Store) LabelGeneration() uint64 {
	s.ensureSelection()
	return s.labelGen.Load()
}

// RouteGeneration counts the route-answer writes (the certified route and
// its preview table) in this process. It is deliberately separate from
// Generation: a layer-1 commit replaces the route rows without touching
// the mail, so a view of the route (the triage page and summary, the label
// queue) must be rebuilt while the expensive triage core built from the
// mail stays valid.
//
// CommitLayer1 bumps it after its transaction commits, so a reader either
// sees the old counter with the old rows or the new counter with the new
// rows, never the new counter before the rows are visible.
func (s *Store) RouteGeneration() uint64 { return s.routeGen.Load() }

// ensureSelection applies a changed selection for the generation
// accessors (which return no error): a failure is logged, and the next
// reader of visible mail reports it.
func (s *Store) ensureSelection() {
	if err := s.applySelection(context.Background()); err != nil {
		s.log.Warn("mailbox selection not applied", "err", err)
	}
}

// HoldMailWrites keeps the mail generation stable while a producer writes a
// run of message or detail batches, then bumps it once when the last hold
// releases. It is the one coalescing owner of high-volume writes: a folder
// sync or a detail pass writes many batches but invalidates every derived
// view once, at its boundary. The returned function is idempotent, so a
// defer plus an explicit early release cannot double-bump.
func (s *Store) HoldMailWrites() func() {
	s.mailHoldMu.Lock()
	s.mailHolds++
	s.mailHoldMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mailHoldMu.Lock()
			s.mailHolds--
			bump := s.mailHolds == 0 && s.mailDirty
			if bump {
				s.mailDirty = false
			}
			s.mailHoldMu.Unlock()
			if bump {
				s.gen.Add(1)
				s.mailGen.Add(1)
			}
		})
	}
}

// touchMail is the high-volume mail write: it bumps both generations at once
// unless a producer holds them. Selection, identity, source and world writes
// keep using touch and are never delayed by a mail hold.
func (s *Store) touchMail() {
	s.mailHoldMu.Lock()
	if s.mailHolds > 0 {
		s.mailDirty = true
		s.mailHoldMu.Unlock()
		return
	}
	s.mailHoldMu.Unlock()
	s.gen.Add(1)
	s.mailGen.Add(1)
}

func (s *Store) touch() {
	s.gen.Add(1)
	s.mailGen.Add(1)
}

// touchVisibility marks a change to the visible population: the mailbox
// selection, a scope verdict, or a folder/message removal. Label views join
// folders, messages and scope verdicts, so they must rebuild even though no
// label row changed; ordinary mail writes still leave LabelGeneration alone.
func (s *Store) touchVisibility() {
	s.gen.Add(1)
	s.mailGen.Add(1)
	s.labelGen.Add(1)
}

// touchLabels marks a label write (Generation and LabelGeneration).
func (s *Store) touchLabels() {
	s.gen.Add(1)
	s.labelGen.Add(1)
}

// touchRoutes marks a route-answer write (RouteGeneration only): the mail
// and its labels did not change, so the core built from them stays valid.
func (s *Store) touchRoutes() { s.routeGen.Add(1) }
