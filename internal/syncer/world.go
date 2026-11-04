package syncer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LLM-4-People/mailsense/internal/calendar"
	"github.com/LLM-4-People/mailsense/internal/contacts"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// CollectionReport is the outcome of one address book or calendar read.
type CollectionReport struct {
	Kind  string `json:"kind"` // contacts | calendar
	Store string `json:"store"`
	Name  string `json:"name"`
	Items int    `json:"items"`
	Error string `json:"error,omitempty"`
	// Incomplete marks a calendar read that skipped an appointment or a
	// recurring series. Its read events were merged into the stored window;
	// occurrences it did not cover were kept, so the window is not complete.
	Incomplete    bool `json:"incomplete,omitempty"`
	SkippedEvents int  `json:"skipped_events,omitempty"`
	SkippedSeries int  `json:"skipped_series,omitempty"`
}

// WorldReport is the non-mail half of a run: address books and calendars.
// A failure here never invalidates the mail that was already written; it is
// reported per collection and retried next run.
type WorldReport struct {
	Contacts            int `json:"contacts"`
	ContactCollections  int `json:"contact_collections"`
	Events              int `json:"events"`
	CalendarCollections int `json:"calendar_collections"`
	// CalendarIncomplete counts calendar reads whose events were merged but
	// whose window is not complete. They are not included in
	// CalendarCollections.
	CalendarIncomplete int                `json:"calendar_incomplete,omitempty"`
	Since              string             `json:"since,omitempty"`
	Until              string             `json:"until,omitempty"`
	Collections        []CollectionReport `json:"collections"`
	Duration           time.Duration      `json:"duration"`
}

// syncWorld reads the connector's address books and calendars. Contacts are
// replaced per store only after a complete read; calendar occurrences are
// replaced inside the configured bounded window only after a complete read,
// and merged (never deleted) after an incomplete one.
func (s *Syncer) syncWorld(ctx context.Context, c source.Connector, rep *Report) WorldReport {
	out := WorldReport{Duration: 0}
	start := s.clock.Now()
	caps := c.Capabilities()
	if cs, ok := c.(source.ContactSource); ok && caps.Contacts && s.cfg.Bool(SettingContactsSyncEnabled) {
		cols, err := cs.ContactCollections(ctx)
		if err != nil {
			out.Collections = append(out.Collections, CollectionReport{Kind: "contacts", Error: err.Error()})
			s.log.Warn("world sync: address books unreadable", "err", err)
		}
		s.log.Info("world sync: address books", "collections", len(cols))
		for _, col := range cols {
			items, err := s.syncContactCollection(ctx, cs, col)
			cr := CollectionReport{Kind: "contacts", Store: col.StoreKey, Name: col.Name, Items: items}
			if err != nil {
				cr.Error = err.Error()
				s.log.Warn("contacts collection failed", "collection", col.Name, "err", err)
			}
			out.Collections = append(out.Collections, cr)
			s.log.Info("world sync: address book read", "collection", col.Name, "items", items, "err", cr.Error)
			out.Contacts += items
			if err == nil {
				out.ContactCollections++
			}
		}
	}
	if cs, ok := c.(source.CalendarSource); ok && caps.Calendar && s.cfg.Bool(SettingCalendarSyncEnabled) {
		since, until := s.calendarWindow()
		out.Since, out.Until = since.UTC().Format(time.RFC3339), until.UTC().Format(time.RFC3339)
		cols, err := cs.CalendarCollections(ctx)
		if err != nil {
			out.Collections = append(out.Collections, CollectionReport{Kind: "calendar", Error: err.Error()})
			s.log.Warn("world sync: calendars unreadable", "err", err)
		}
		s.log.Info("world sync: calendars", "collections", len(cols), "since", out.Since, "until", out.Until)
		for _, col := range cols {
			read := s.syncCalendarCollection(ctx, cs, col, since, until)
			cr := CollectionReport{Kind: "calendar", Store: col.StoreKey, Name: col.Name, Items: read.events,
				Incomplete: read.incomplete, SkippedEvents: read.skippedEvents, SkippedSeries: read.skippedSeries}
			if read.err != nil {
				cr.Error = read.err.Error()
				s.log.Warn("calendar collection failed", "collection", col.Name, "err", read.err)
			}
			if read.incomplete {
				s.log.Warn("world sync: calendar read incomplete; stored occurrences kept", "collection", col.Name,
					"store", col.StoreKey, "events", read.events, "skipped_events", read.skippedEvents,
					"skipped_series", read.skippedSeries)
			}
			out.Collections = append(out.Collections, cr)
			s.log.Info("world sync: calendar read", "collection", col.Name, "items", read.events,
				"incomplete", cr.Incomplete, "skipped_events", cr.SkippedEvents, "skipped_series", cr.SkippedSeries,
				"err", cr.Error)
			out.Events += read.events
			if read.err == nil {
				if read.incomplete {
					out.CalendarIncomplete++
				} else {
					out.CalendarCollections++
				}
			}
		}
	}
	out.Duration = s.clock.Now().Sub(start)
	s.log.Info("world sync finished", "contacts", out.Contacts, "contact_collections", out.ContactCollections,
		"events", out.Events, "calendar_collections", out.CalendarCollections,
		"calendar_incomplete", out.CalendarIncomplete, "duration", out.Duration)
	if rep != nil {
		rep.World = &out
	}
	return out
}

// calendarWindow is the bounded occurrence window the store keeps. The
// defaults cover the recent past (overdue commitments) and the planning
// horizon; both are settings, never constants in the connector.
func (s *Syncer) calendarWindow() (time.Time, time.Time) {
	now := s.clock.Now()
	past := time.Duration(s.cfg.Int(SettingCalendarPastDays)) * 24 * time.Hour
	future := time.Duration(s.cfg.Int(SettingCalendarFutureDays)) * 24 * time.Hour
	return now.Add(-past), now.Add(future)
}

type contactBuffer struct {
	list []contacts.Contact
}

func (b *contactBuffer) Contact(_ context.Context, c contacts.Contact) error {
	b.list = append(b.list, c)
	return nil
}

func (b *contactBuffer) Progress(source.Progress) {}

func (s *Syncer) syncContactCollection(ctx context.Context, cs source.ContactSource, col source.ContactCollection) (int, error) {
	buf := &contactBuffer{}
	if err := cs.SyncContacts(ctx, col, buf); err != nil {
		return 0, err
	}
	ref := store.StoreRef{SourceID: col.SourceID, StoreID: col.StoreID}
	if err := s.store.ReplaceContacts(ctx, ref, col.Path, buf.list); err != nil {
		return 0, err
	}
	return len(buf.list), nil
}

type calendarBuffer struct {
	list []calendar.Event
}

func (b *calendarBuffer) Event(_ context.Context, e calendar.Event) error {
	if err := e.Validate(); err != nil {
		return err
	}
	b.list = append(b.list, e)
	return nil
}

func (b *calendarBuffer) Progress(source.Progress) {}

// calendarIncomplete is the optional partial-read signal a CalendarSource may
// return. The Outlook connector's IncompleteCalendarError implements it. The
// source has already streamed every readable event when it returns this
// error, so the syncer merges instead of replacing the window.
type calendarIncomplete interface {
	error
	CalendarSkipped() (events, series int)
}

// calendarRead is the outcome of one calendar read before it is turned into
// the public CollectionReport.
type calendarRead struct {
	events        int
	incomplete    bool
	skippedEvents int
	skippedSeries int
	err           error
}

func (s *Syncer) syncCalendarCollection(ctx context.Context, cs source.CalendarSource, col source.CalendarCollection,
	since, until time.Time) calendarRead {
	buf := &calendarBuffer{}
	var read calendarRead
	if err := cs.SyncCalendar(ctx, col, since, until, buf); err != nil {
		var partial calendarIncomplete
		if !errors.As(err, &partial) {
			read.err = err
			return read
		}
		read.incomplete = true
		read.skippedEvents, read.skippedSeries = partial.CalendarSkipped()
	}
	ref := store.StoreRef{SourceID: col.SourceID, StoreID: col.StoreID}
	cal := store.CalendarRef{EntryID: col.EntryID, Name: col.Name, Default: col.Default}
	var err error
	if read.incomplete {
		err = s.store.MergeCalendarWindow(ctx, ref, cal, since, until, buf.list)
	} else {
		err = s.store.ReplaceCalendarWindow(ctx, ref, cal, since, until, buf.list)
	}
	if err != nil {
		read.err = fmt.Errorf("calendar %s: %w", col.Name, err)
		return read
	}
	read.events = len(buf.list)
	return read
}
