package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/LLM-4-People/mailsense/internal/agenda"
	"github.com/LLM-4-People/mailsense/internal/calendar"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/contacts"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// CalendarRef identifies the calendar folder a window belongs to.
type CalendarRef struct {
	EntryID string
	Name    string
	Default bool
}

// ReplaceContacts replaces one address-book folder in a single
// transaction. The caller must have read the whole folder (a partial read
// would delete contacts it never saw); the syncer enforces that contract.
// The delete is scoped to the folder: a store can hold several address
// books (the default one, an Imported folder, team books).
func (s *Store) ReplaceContacts(ctx context.Context, ref StoreRef, folderPath string, list []contacts.Contact) error {
	defer s.touch()
	key := sourceStoreKey(ref)
	for _, c := range list {
		if c.FolderPath != "" && folderPath != "" && c.FolderPath != folderPath {
			return fmt.Errorf("store: contact %q belongs to %q, not %q", c.EntryID, c.FolderPath, folderPath)
		}
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM contacts
			WHERE source_id = ? AND store_id = ? AND folder_path = ?`,
			ref.SourceID, ref.StoreID, folderPath); err != nil {
			return fmt.Errorf("store: clear contacts: %w", err)
		}
		for _, c := range list {
			if strings.TrimSpace(c.EntryID) == "" {
				continue
			}
			if c.FolderPath == "" {
				c.FolderPath = folderPath
			}
			res, err := tx.ExecContext(ctx, `INSERT INTO contacts
				(source_id, store_id, store_key, entry_id, revision, display_name, company, department, job_title,
				 phone, folder_path, last_modified)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				ref.SourceID, ref.StoreID, key, c.EntryID, c.Revision, c.DisplayName, c.Company, c.Department,
				c.JobTitle, contacts.CleanPhone(c.Phone), c.FolderPath, c.LastModified)
			if err != nil {
				return fmt.Errorf("store: insert contact: %w", err)
			}
			id, err := res.LastInsertId()
			if err != nil {
				return fmt.Errorf("store: contact id: %w", err)
			}
			for i, a := range c.Addresses() {
				if _, err := tx.ExecContext(ctx, `INSERT INTO contact_emails (contact_id, ordinal, address, address_norm)
					VALUES (?, ?, ?, ?)`, id, i, a, a); err != nil {
					return fmt.Errorf("store: insert contact email: %w", err)
				}
			}
		}
		return nil
	})
}

// Contacts returns every address-book entry of the visible stores, with its
// addresses in source order.
func (s *Store) Contacts(ctx context.Context) ([]contacts.Contact, error) {
	keys := s.pickedKeys()
	out, err := query[contacts.Contact](ctx, s.db, `SELECT c.source_id, c.store_id, c.store_key, c.entry_id, c.revision,
		c.display_name, c.company, c.department, c.job_title, c.phone, c.folder_path, c.last_modified,
		COALESCE((SELECT json_group_array(ce.address) FROM
			(SELECT address FROM contact_emails WHERE contact_id = c.id ORDER BY ordinal) ce), '[]')
		FROM contacts c
		WHERE c.store_key IN `+stringSetSQL+`
		ORDER BY c.display_name, c.entry_id`, []any{jsonList(keys)}, func(rows *sql.Rows, c *contacts.Contact) error {
		var emails string
		if err := rows.Scan(&c.SourceID, &c.StoreID, &c.StoreKey, &c.EntryID, &c.Revision, &c.DisplayName,
			&c.Company, &c.Department, &c.JobTitle, &c.Phone, &c.FolderPath, &c.LastModified, &emails); err != nil {
			return err
		}
		return json.Unmarshal([]byte(emails), &c.Emails)
	})
	if err != nil {
		return nil, fmt.Errorf("store: contacts: %w", err)
	}
	if out == nil {
		out = []contacts.Contact{}
	}
	return out, nil
}

// ReplaceCalendarWindow replaces the occurrences of one calendar that start
// inside [since, until). The connector expands recurrences before calling
// this, so the store never interprets a recurrence rule. Occurrences that
// moved into the window are inserted; stale ones are removed by the range
// delete. Call it only after a complete read.
func (s *Store) ReplaceCalendarWindow(ctx context.Context, ref StoreRef, cal CalendarRef,
	since, until time.Time, events []calendar.Event) error {
	return s.writeCalendarWindow(ctx, ref, cal, since, until, events, true)
}

// MergeCalendarWindow upserts the occurrences an incomplete read delivered
// and deletes nothing: an occurrence the read did not cover is treated as
// unknown, not vanished. The next complete read calls ReplaceCalendarWindow
// and removes what truly disappeared. Use this for a source read that
// reported skipped appointments or series.
func (s *Store) MergeCalendarWindow(ctx context.Context, ref StoreRef, cal CalendarRef,
	since, until time.Time, events []calendar.Event) error {
	return s.writeCalendarWindow(ctx, ref, cal, since, until, events, false)
}

// writeCalendarWindow is the one calendar-window writer. complete selects the
// replacement semantics: only a complete read may delete the window's absent
// occurrences or trim occurrences that slid outside it.
func (s *Store) writeCalendarWindow(ctx context.Context, ref StoreRef, cal CalendarRef,
	since, until time.Time, events []calendar.Event, complete bool) error {
	if !since.Before(until) {
		return fmt.Errorf("store: calendar window must be non-empty")
	}
	defer s.touch()
	key := sourceStoreKey(ref)
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var calID int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM calendars WHERE source_id = ? AND store_id = ? AND entry_id = ?`,
			ref.SourceID, ref.StoreID, cal.EntryID).Scan(&calID)
		switch {
		case err == sql.ErrNoRows:
			res, err := tx.ExecContext(ctx, `INSERT INTO calendars (source_id, store_id, store_key, entry_id, name, is_default)
				VALUES (?, ?, ?, ?, ?, ?)`, ref.SourceID, ref.StoreID, key, cal.EntryID, cal.Name, boolInt(cal.Default))
			if err != nil {
				return fmt.Errorf("store: insert calendar: %w", err)
			}
			if calID, err = res.LastInsertId(); err != nil {
				return fmt.Errorf("store: calendar id: %w", err)
			}
		case err != nil:
			return fmt.Errorf("store: read calendar: %w", err)
		default:
			if _, err := tx.ExecContext(ctx, "UPDATE calendars SET name = ?, is_default = ? WHERE id = ?",
				cal.Name, boolInt(cal.Default), calID); err != nil {
				return fmt.Errorf("store: update calendar: %w", err)
			}
		}
		if complete {
			// Overlap, not start-inside: the reader also returns an occurrence
			// that began before the window and ends inside it. Matching the read
			// predicate keeps the stored window exactly what the source covered.
			// One calendar's window only: the same store may hold several
			// calendars, and syncing one must not wipe the others' events.
			if _, err := tx.ExecContext(ctx, `DELETE FROM calendar_events
				WHERE source_id = ? AND store_key = ? AND calendar_id = ? AND end_at > ? AND start_at < ?`,
				ref.SourceID, key, calID, instantString(since), instantString(until)); err != nil {
				return fmt.Errorf("store: clear calendar window: %w", err)
			}
			// The window slides with the clock. An occurrence wholly outside it
			// is no longer covered by the source and would otherwise be retained
			// forever, growing the table and widening every later read.
			if _, err := tx.ExecContext(ctx, `DELETE FROM calendar_events
				WHERE source_id = ? AND store_key = ? AND calendar_id = ? AND (end_at <= ? OR start_at >= ?)`,
				ref.SourceID, key, calID, instantString(since), instantString(until)); err != nil {
				return fmt.Errorf("store: trim calendar window: %w", err)
			}
		}
		for _, e := range events {
			if err := e.Validate(); err != nil {
				return err
			}
			var id int64
			if err := tx.QueryRowContext(ctx, `INSERT INTO calendar_events
				(calendar_id, source_id, store_key, entry_id, global_id, clean_global_id, subject, location,
				 organizer_name, organizer_address, start_at, end_at, all_day, busy_status, meeting_status,
				 response_status, is_recurring, series_id, occurrence_index, online_url, body_preview, last_modified)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (source_id, store_key, entry_id, start_at) DO UPDATE SET
					subject = excluded.subject, location = excluded.location,
					organizer_name = excluded.organizer_name, organizer_address = excluded.organizer_address,
					end_at = excluded.end_at, all_day = excluded.all_day, busy_status = excluded.busy_status,
					meeting_status = excluded.meeting_status, response_status = excluded.response_status,
					is_recurring = excluded.is_recurring, series_id = excluded.series_id,
					occurrence_index = excluded.occurrence_index, online_url = excluded.online_url,
					body_preview = excluded.body_preview, last_modified = excluded.last_modified
				RETURNING id`,
				calID, ref.SourceID, key, e.EntryID, e.GlobalID, e.CleanGlobalID, e.Subject, e.Location,
				e.OrganizerName, contacts.Normalize(e.OrganizerEmail), instantString(e.Start), instantString(e.End),
				boolInt(e.AllDay), int(e.Busy), int(e.Meeting), int(e.Response), boolInt(e.Recurring),
				e.SeriesID, e.Occurrence, e.OnlineURL, e.BodyPreview, e.LastModified).Scan(&id); err != nil {
				return fmt.Errorf("store: insert event: %w", err)
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM event_attendees WHERE event_id = ?", id); err != nil {
				return fmt.Errorf("store: clear attendees: %w", err)
			}
			for _, a := range e.Attendees {
				if _, err := tx.ExecContext(ctx, `INSERT INTO event_attendees
					(event_id, kind, name, address, address_norm, response_status) VALUES (?, ?, ?, ?, ?, ?)`,
					id, string(a.Kind), a.Name, a.Address, contacts.Normalize(a.Address), a.ResponseStatus); err != nil {
					return fmt.Errorf("store: insert attendee: %w", err)
				}
			}
		}
		return nil
	})
}

// CalendarEvents returns the visible stores' occurrences overlapping
// [since, until), attendees included, ordered by start.
func (s *Store) CalendarEvents(ctx context.Context, since, until time.Time) ([]calendar.Event, error) {
	keys := s.pickedKeys()
	rows, err := s.db.QueryContext(ctx, `SELECT e.id, e.source_id, e.store_key, e.calendar_id, e.entry_id, e.global_id,
		e.clean_global_id, e.subject, e.location, e.organizer_name, e.organizer_address, e.start_at, e.end_at,
		e.all_day, e.busy_status, e.meeting_status, e.response_status, e.is_recurring, e.series_id,
		e.occurrence_index, e.online_url, e.body_preview, e.last_modified
		FROM calendar_events e
		WHERE e.store_key IN `+stringSetSQL+`
			AND e.start_at < ? AND e.end_at > ?
		ORDER BY e.start_at, e.end_at, e.entry_id`,
		jsonList(keys), instantString(until), instantString(since))
	if err != nil {
		return nil, fmt.Errorf("store: calendar events: %w", err)
	}
	defer rows.Close()
	out := []calendar.Event{}
	var ids []int64
	for rows.Next() {
		var e calendar.Event
		var eventID int64
		var start, end string
		var allDay, recurring int
		var busy, meeting, response int
		if err := rows.Scan(&eventID, &e.SourceID, &e.StoreKey, &e.CalendarID, &e.EntryID, &e.GlobalID, &e.CleanGlobalID,
			&e.Subject, &e.Location, &e.OrganizerName, &e.OrganizerEmail, &start, &end, &allDay, &busy, &meeting,
			&response, &recurring, &e.SeriesID, &e.Occurrence, &e.OnlineURL, &e.BodyPreview, &e.LastModified); err != nil {
			return nil, fmt.Errorf("store: calendar row: %w", err)
		}
		if t, ok := mail.Parse(start); ok {
			e.Start = t
		}
		if t, ok := mail.Parse(end); ok {
			e.End = t
		}
		e.AllDay, e.Recurring = allDay == 1, recurring == 1
		e.Busy, e.Meeting, e.Response = calendar.BusyStatus(busy), calendar.MeetingStatus(meeting), calendar.ResponseStatus(response)
		ids = append(ids, eventID)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: calendar rows: %w", err)
	}
	if len(out) == 0 {
		return out, nil
	}
	atts, err := s.attendees(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Attendees = atts[ids[i]]
	}
	return out, nil
}

// attendeesQuery is the one query shape of the window attendee read. It takes
// the event ids the window returned, never the visible calendar: a calendar
// may hold events outside the requested window, and reading their attendees
// would make this read scale with the whole calendar instead of the window.
// The id list is one JSON parameter, so any window size stays one statement.
const attendeesQuery = `SELECT a.event_id, a.kind, a.name, a.address, a.response_status
	FROM event_attendees a INDEXED BY event_attendees_event
	WHERE a.event_id IN ` + stringSetSQL + `
	ORDER BY a.event_id, a.kind, a.address`

// attendees reads the attendees of the given events in one indexed range
// read, keyed by event id.
func (s *Store) attendees(ctx context.Context, eventIDs []int64) (map[int64][]calendar.Attendee, error) {
	out := map[int64][]calendar.Attendee{}
	if len(eventIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, attendeesQuery, idSet(eventIDs))
	if err != nil {
		return nil, fmt.Errorf("store: attendees: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var eventID int64
		var kind, name, address string
		var response int
		if err := rows.Scan(&eventID, &kind, &name, &address, &response); err != nil {
			return nil, fmt.Errorf("store: attendee row: %w", err)
		}
		out[eventID] = append(out[eventID], calendar.Attendee{Kind: calendar.AttendeeKind(kind), Name: name,
			Address: address, ResponseStatus: response})
	}
	return out, rows.Err()
}

// OwnerSet is the union of every visible store's owner addresses, in
// matching form. It is the one value the world model excludes when it asks
// "who else is in this thread or meeting".
func (s *Store) OwnerSet(ctx context.Context) (map[string]bool, error) {
	owners, err := s.Owners(ctx)
	if err != nil {
		return nil, err
	}
	keys := s.pickedKeys()
	out := map[string]bool{}
	for _, k := range keys {
		for _, a := range owners[k].Identity.Addresses {
			out[contacts.Normalize(a)] = true
		}
	}
	return out, nil
}

// threadSummariesQuery is the one query shape of the thread summary read.
// It pins the outer loop to the requested conversations: without the hint the
// planner starts from scope_verdicts(verdict='owner'), which is almost the
// whole mailbox, and visits every owner message for every call. The pinned
// index carries the instant and row id, so the rows arrive newest first and
// the first row of a conversation is its newest member; the plain
// conversation index would sort every batch for the last two order terms.
func threadSummariesQuery(vis string) string {
	return `SELECT m.id, m.conversation_id, m.subject, m.sender_name, m.sender_address,
		` + instantSQL + `, ` + recipientsSQL + `
		FROM messages m INDEXED BY messages_conversation_instant
		JOIN folders f ON f.id = m.folder_id
		JOIN scope_verdicts v ON v.message_id = m.id
		WHERE ` + ownerFacingWhere(vis) + ` AND m.conversation_id IN ` + stringSetSQL + `
		ORDER BY m.conversation_id, ` + instantSQL + ` DESC, m.id DESC`
}

// ThreadSummaries reads the owner-facing members of the requested
// conversations and returns the subject of the newest one plus every
// non-owner participant. A shared/delegate store never contributes a
// subject or participant: it may change state, not content. Conversations
// with no owner-facing member are absent.
func (s *Store) ThreadSummaries(ctx context.Context, conversationIDs []string) (map[string]agenda.ThreadSummary, error) {
	if len(conversationIDs) == 0 {
		return map[string]agenda.ThreadSummary{}, nil
	}
	vis, err := s.visible(ctx)
	if err != nil {
		return nil, err
	}
	owner, err := s.OwnerSet(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, threadSummariesQuery(vis), jsonList(conversationIDs))
	if err != nil {
		return nil, fmt.Errorf("store: thread summaries: %w", err)
	}
	defer rows.Close()
	out := map[string]agenda.ThreadSummary{}
	people := map[string]map[string]bool{}
	for rows.Next() {
		var id int64
		var conv, subject, name, address, instant, recips string
		if err := rows.Scan(&id, &conv, &subject, &name, &address, &instant, &recips); err != nil {
			return nil, fmt.Errorf("store: thread summary row: %w", err)
		}
		if conv == "" {
			continue
		}
		// The query returns each conversation newest first: only the first
		// row may name the subject and instant. Participants below are a set,
		// so they still accumulate from every member.
		if _, seen := out[conv]; !seen {
			out[conv] = agenda.ThreadSummary{ConversationID: conv, Subject: subject, MessageID: id, LastAt: instant}
		}
		if people[conv] == nil {
			people[conv] = map[string]bool{}
		}
		parsed, err := parseRecipients(recips)
		if err != nil {
			return nil, err
		}
		for address := range externalAddresses(address, parsed, owner) {
			people[conv][address] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: thread summary rows: %w", err)
	}
	for conv, set := range people {
		list := make([]string, 0, len(set))
		for a := range set {
			list = append(list, a)
		}
		sort.Strings(list)
		s := out[conv]
		s.Participants = list
		out[conv] = s
	}
	return out, nil
}

// DueDeadlines reads owner-facing mail whose follow-up flag is due inside
// [since, until), earliest due first. A completed follow-up (flag status
// complete, 1) is not due and is excluded here, the one read every deadline
// consumer uses. A shared/delegate store never contributes a deadline: its
// subject, sender and participants are not the owner's content. A message
// without participants still counts: its sender is a participant.
// dueDeadlinesQuery is the one query shape of the open-follow-up read. The
// partial index messages_task_due contains only messages that carry a due
// date, so the window is an index range probe instead of a walk over every
// owner message.
func dueDeadlinesQuery(vis string) string {
	return `SELECT m.id, m.conversation_id, m.subject, m.sender_name, m.sender_address,
		m.task_due_date, ` + recipientsSQL + `
		FROM messages m INDEXED BY messages_task_due
		JOIN folders f ON f.id = m.folder_id
		JOIN scope_verdicts v ON v.message_id = m.id
		WHERE ` + ownerFacingWhere(vis) + ` AND m.task_due_date <> ''
			AND COALESCE(m.flag_status, 0) <> 1
			AND m.task_due_date >= ? AND m.task_due_date <= ?
		ORDER BY m.task_due_date, m.id`
}

func (s *Store) DueDeadlines(ctx context.Context, since, until time.Time) ([]agenda.Deadline, error) {
	vis, err := s.visible(ctx)
	if err != nil {
		return nil, err
	}
	owner, err := s.OwnerSet(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, dueDeadlinesQuery(vis), instantString(since), instantString(until))
	if err != nil {
		return nil, fmt.Errorf("store: due deadlines: %w", err)
	}
	defer rows.Close()
	out := []agenda.Deadline{}
	for rows.Next() {
		var d agenda.Deadline
		var sender, due, recips string
		if err := rows.Scan(&d.MessageID, &d.ConversationID, &d.Subject, &d.SenderName, &sender, &due, &recips); err != nil {
			return nil, fmt.Errorf("store: deadline row: %w", err)
		}
		if t, ok := mail.Parse(due); ok {
			d.Due = t
		}
		parsed, err := parseRecipients(recips)
		if err != nil {
			return nil, err
		}
		for a := range externalAddresses(sender, parsed, owner) {
			d.Participants = append(d.Participants, a)
		}
		sort.Strings(d.Participants)
		out = append(out, d)
	}
	return out, rows.Err()
}

// pickedKeys returns the selected store keys, sorted for a deterministic
// SQL parameter.
func (s *Store) pickedKeys() []string { return s.pickedKeysOf(s.cfg.Snapshot()) }

// pickedKeysOf is pickedKeys from one configuration snapshot, for a caller
// that already keyed its work by that snapshot.
func (s *Store) pickedKeysOf(snap *config.Snapshot) []string {
	picked := source.Selected(snap)
	out := make([]string, 0, len(picked))
	for k := range picked {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sourceStoreKey(ref StoreRef) string { return source.StoreKey(ref.SourceID, ref.StoreID) }

func instantString(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(mail.TimeLayout)
}
