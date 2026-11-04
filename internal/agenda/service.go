package agenda

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LLM-4-People/mailsense/internal/calendar"
	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
	"github.com/LLM-4-People/mailsense/internal/viewcache"
)

// Store is the world-model read side the agenda service needs.
type Store interface {
	CalendarEvents(ctx context.Context, since, until time.Time) ([]calendar.Event, error)
	DueDeadlines(ctx context.Context, since, until time.Time) ([]Deadline, error)
	ThreadSummaries(ctx context.Context, conversationIDs []string) (map[string]ThreadSummary, error)
	OwnerSet(ctx context.Context) (map[string]bool, error)
	// MailGeneration moves on mail and folder writes; the agenda reads no
	// labels, so a label click must not rebuild the day.
	MailGeneration() uint64
	// RouteGeneration moves when route answers commit: the obligation items
	// carry the priority thread truth, which is re-derived from them.
	RouteGeneration() uint64
}

// Threads is the one thread-truth resolver. The priority service implements
// it, so the agenda never re-derives whether a colleague already answered.
type Threads interface {
	ThreadStates(ctx context.Context) (map[string]threadintel.Assessment, error)
}

// Deps are the service collaborators. Store and Threads are optional: a
// missing one degrades the agenda to the evidence it can see, never to an
// invented item.
type Deps struct {
	Store   Store
	Threads Threads
	Clock   clock.Clock
	// DefaultHorizon is used when the caller asks for zero. The service
	// clamps every request to [MinHorizon, MaxHorizon].
	DefaultHorizon time.Duration
}

// Service builds the "next X" agenda from the stored world model.
type Service struct {
	store   Store
	threads Threads
	clock   clock.Clock
	def     time.Duration

	cache *viewcache.Cache[agendaKey, Result]
}

// agendaKey is one built horizon under the store generations it read.
type agendaKey struct {
	horizon  time.Duration
	mailGen  uint64
	routeGen uint64
}

// Horizon bounds. A day is the largest useful "next X" for a desktop
// assistant; anything longer is a calendar view, not an agenda.
const (
	MinHorizon = time.Hour
	MaxHorizon = 7 * 24 * time.Hour
)

// New returns an agenda service.
func New(d Deps) *Service {
	def := d.DefaultHorizon
	if def <= 0 {
		def = 3 * time.Hour
	}
	def = clampHorizon(def)
	s := &Service{store: d.Store, threads: d.Threads, clock: clock.Or(d.Clock), def: def}
	s.cache = viewcache.New[agendaKey, Result](viewcache.Options[agendaKey, Result]{
		TTL: 15 * time.Second, Size: 8, Now: s.clock.Now, Component: "agenda",
	})
	return s
}

// Close joins the background rebuilds of the agenda cache, bounded by ctx. It
// reports whether they joined: false means a store read outlived ctx, so the
// store owner keeps the store open. It is idempotent.
func (s *Service) Close(ctx context.Context) bool { return s.cache.Close(ctx) }

// DefaultHorizon returns the configured fallback window.
func (s *Service) DefaultHorizon() time.Duration { return s.def }

// Build computes the agenda for the next horizon. A thread read failure
// fails the whole build: showing a calendar while silently dropping the
// owner's obligations would misrepresent the day.
func (s *Service) Build(ctx context.Context, horizon time.Duration) (Result, error) {
	if horizon <= 0 {
		horizon = s.def
	}
	horizon = clampHorizon(horizon)
	if s.store == nil {
		return Result{}, errors.New("agenda: a store is required")
	}
	key := agendaKey{horizon: horizon, mailGen: s.store.MailGeneration(), routeGen: s.store.RouteGeneration()}
	return s.cache.Get(ctx, key, func(ctx context.Context) (Result, error) {
		return s.build(ctx, horizon)
	})
}

func (s *Service) build(ctx context.Context, horizon time.Duration) (Result, error) {
	now := s.clock.Now()
	until := now.Add(horizon)

	owner, err := s.store.OwnerSet(ctx)
	if err != nil {
		return Result{}, err
	}
	events, err := s.store.CalendarEvents(ctx, now.Add(-time.Hour), until)
	if err != nil {
		return Result{}, err
	}
	deadlines, err := s.store.DueDeadlines(ctx, now.Add(-30*24*time.Hour), until)
	if err != nil {
		return Result{}, err
	}
	obligations, err := s.obligations(ctx, now)
	if err != nil {
		return Result{}, err
	}
	return Build(Input{Now: now, Horizon: horizon, Owner: owner, Events: events,
		Obligations: obligations, Deadlines: deadlines}), nil
}

// Debtless is not a field; the helper below keeps Build's input explicit.
func (s *Service) obligations(ctx context.Context, now time.Time) ([]Obligation, error) {
	if s.threads == nil {
		return nil, nil
	}
	states, err := s.threads.ThreadStates(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(states))
	for conv, state := range states {
		if state.NeedsReply() {
			ids = append(ids, conv)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	summaries, err := s.store.ThreadSummaries(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]Obligation, 0, len(ids))
	for _, conv := range ids {
		state := states[conv]
		sum := summaries[conv]
		o := Obligation{ConversationID: conv, Subject: sum.Subject, MessageID: state.ReplyToMessageID,
			ActorName: state.OwnerActorName, Participants: sum.Participants}
		if o.MessageID == 0 {
			o.MessageID = sum.MessageID
		}
		if t, ok := mail.Parse(sum.LastAt); ok {
			o.LastAt = t
		}
		if o.LastAt.IsZero() {
			o.LastAt = now
		}
		out = append(out, o)
	}
	return out, nil
}

func clampHorizon(h time.Duration) time.Duration {
	if h < MinHorizon {
		return MinHorizon
	}
	if h > MaxHorizon {
		return MaxHorizon
	}
	return h
}

// Hours validates and converts a user-facing hour count. It is the one
// place the API, the planner and the settings agree on the bound.
func Hours(h float64) (time.Duration, error) {
	if h <= 0 {
		return 0, fmt.Errorf("agenda: horizon must be positive, got %g", h)
	}
	d := time.Duration(h * float64(time.Hour))
	return clampHorizon(d), nil
}
