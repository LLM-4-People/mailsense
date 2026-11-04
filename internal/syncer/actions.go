package syncer

import (
	"context"
	"errors"
	"fmt"

	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// The selection and identity actions of the picking screen. Each checks
// its keys against the discovered mailboxes, saves the change to
// settings.json and publishes the new views (TopicMailboxes). The store
// applies a new selection at its next read, so an unpicked store's mail
// leaves search, vectors, scope and the learners at once.

// Pick adds discovered stores to the selection and returns the mailbox views
// the change produced, the same snapshot it publishes on TopicMailboxes.
func (s *Syncer) Pick(ctx context.Context, keys ...string) ([]store.MailboxView, error) {
	return s.edit(ctx, func(known []source.Mailbox) error { return source.Pick(s.cfg, known, keys...) })
}

// StartPicked starts one sync job per source with a picked mailbox: the
// first sync fills mail, stores, owner identity and scope without a second
// owner action. views is the snapshot Pick (or Unpick, Declare, Undeclare)
// just returned and published: the caller already read it for its response,
// so the start maps the selection to sources from it instead of scanning
// MailboxViews again. nil means the caller has no snapshot (the automatic
// first pass after a restart) and the views are read here once. A source
// already syncing (ErrSourceBusy) is benign, each source gets at most one
// job, and the selection is never changed here. Job-start failures are logged
// and returned for the caller to report, while the pick itself stays
// committed.
func (s *Syncer) StartPicked(ctx context.Context, m *jobs.Manager, connectors []source.Connector, views []store.MailboxView) ([]*jobs.Job, error) {
	if views == nil {
		var err error
		if views, err = s.store.MailboxViews(ctx); err != nil {
			return nil, err
		}
	}
	return s.startPicked(m, connectors, views, true, "first sync")
}

// StartPickedMail starts the picked sources' mail pass only. The automatic
// path uses it after its first full pass, so calendars and address books are
// not re-read on every polling interval. It has no caller-held snapshot, so it
// reads the views once here.
func (s *Syncer) StartPickedMail(ctx context.Context, m *jobs.Manager, connectors []source.Connector) ([]*jobs.Job, error) {
	views, err := s.store.MailboxViews(ctx)
	if err != nil {
		return nil, err
	}
	return s.startPicked(m, connectors, views, false, "automatic sync")
}

func (s *Syncer) startPicked(m *jobs.Manager, connectors []source.Connector, views []store.MailboxView, world bool, label string) ([]*jobs.Job, error) {
	bySource := map[string]bool{}
	for _, v := range views {
		if v.Selected && v.SourceID != "" {
			bySource[v.SourceID] = true
		}
	}
	var started []*jobs.Job
	var errs []error
	for _, c := range connectors {
		if !bySource[c.ID()] {
			continue
		}
		j, err := s.start(m, c, world)
		if err != nil {
			if errors.Is(err, ErrSourceBusy) {
				s.log.Info(label+" already running for the source", "source", c.ID())
				continue
			}
			s.log.Error("cannot start the "+label, "source", c.ID(), "err", err)
			errs = append(errs, fmt.Errorf("%s: %w", c.ID(), err))
			continue
		}
		started = append(started, j)
	}
	return started, errors.Join(errs...)
}

// Unpick takes stores out of the selection (their mail is kept) and returns
// the mailbox views the change produced.
func (s *Syncer) Unpick(ctx context.Context, keys ...string) ([]store.MailboxView, error) {
	return s.edit(ctx, func([]source.Mailbox) error { return source.Unpick(s.cfg, keys...) })
}

// Declare adds an owner address of a discovered store and returns the mailbox
// views the change produced.
func (s *Syncer) Declare(ctx context.Context, key, address string) ([]store.MailboxView, error) {
	return s.edit(ctx, func(known []source.Mailbox) error { return source.Declare(s.cfg, known, key, address) })
}

// Undeclare removes a declared owner address of a store and returns the
// mailbox views the change produced.
func (s *Syncer) Undeclare(ctx context.Context, key, address string) ([]store.MailboxView, error) {
	return s.edit(ctx, func([]source.Mailbox) error { return source.Undeclare(s.cfg, key, address) })
}

// Mailboxes is the picking screen's query (store.MailboxViews).
func (s *Syncer) Mailboxes(ctx context.Context) ([]store.MailboxView, error) {
	return s.store.MailboxViews(ctx)
}

// edit applies one selection or identity change and returns the views it
// produced. The post-change read is the one the screen receives and the one
// published on TopicMailboxes: callers must not read MailboxViews again for
// the same change, or the event and the response can differ and the scan runs
// twice.
func (s *Syncer) edit(ctx context.Context, change func(known []source.Mailbox) error) ([]store.MailboxView, error) {
	views, err := s.store.MailboxViews(ctx)
	if err != nil {
		return nil, err
	}
	var known []source.Mailbox
	for _, v := range views {
		if v.Discovered {
			known = append(known, v.Mailbox)
		}
	}
	if err := change(known); err != nil {
		return nil, err
	}
	if views, err = s.store.MailboxViews(ctx); err != nil {
		return nil, err
	}
	if s.bus != nil {
		events.Publish(s.bus, TopicMailboxes, views)
	}
	s.log.Info("mailbox selection or identity changed", "picked", len(source.Selected(s.cfg.Snapshot())))
	return views, nil
}
