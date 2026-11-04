package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/LLM-4-People/mailsense/internal/contacts"
	"github.com/LLM-4-People/mailsense/internal/mail"
)

// Conversation ids are opaque source keys. The bound keeps one malformed
// request from turning into a pathological SQL parameter or an unbounded
// render, without guessing at the source's own id shape.
const (
	// MaxConversationIDChars is the longest conversation id a read accepts.
	MaxConversationIDChars = 512
	// MaxThreadMessages bounds one conversation page.
	MaxThreadMessages = 100
	// bodyPrefix is the SQL bound on the body text a row read copies out
	// of the store, for a conversation read and a mail-list page alike.
	// The full body never leaves these reads.
	bodyPrefix = 600
	// threadSnippetRunes bounds the collapsed plain-text snippet the UI
	// shows; it is always shorter than bodyPrefix.
	threadSnippetRunes = 200
)

var (
	// ErrThreadNotFound is returned when a conversation has no message the
	// owner may see. It is deliberately distinct from ErrNotFound: the API
	// answers it as unknown_thread, not as an unknown message.
	ErrThreadNotFound = errors.New("store: thread not found")
	// ErrInvalidConversationID is returned for an empty id, an id longer
	// than MaxConversationIDChars, or an id carrying a control character.
	ErrInvalidConversationID = errors.New("store: invalid conversation id")
	// ErrInvalidThreadCursor is returned when a before id does not name an
	// owner-visible message of that conversation. A cursor from another
	// conversation (or a shared row) can never page this one.
	ErrInvalidThreadCursor = errors.New("store: invalid thread cursor")
)

// ValidateConversationID is the one conversation-id contract of the store
// read and the HTTP route.
func ValidateConversationID(id string) error {
	if id == "" {
		return fmt.Errorf("%w: empty", ErrInvalidConversationID)
	}
	if utf8.RuneCountInString(id) > MaxConversationIDChars {
		return fmt.Errorf("%w: longer than %d characters", ErrInvalidConversationID, MaxConversationIDChars)
	}
	if strings.ContainsFunc(id, unicode.IsControl) {
		return fmt.Errorf("%w: contains a control character", ErrInvalidConversationID)
	}
	return nil
}

// ThreadMessage is one owner-visible conversation member: metadata plus a
// bounded, collapsed plain-text snippet. BodyHTML is never read here.
type ThreadMessage struct {
	ID             int64  `json:"id"`
	ConversationID string `json:"conversation_id"`
	Subject        string `json:"subject"`
	SenderName     string `json:"sender_name"`
	SenderAddress  string `json:"sender_address"`
	// ReceivedAt is the mail instant: sent_on for a sent item, received_at
	// otherwise (the same instant priority orders by).
	ReceivedAt     string `json:"received_at"`
	Sent           bool   `json:"sent"`
	IsRead         bool   `json:"is_read"`
	HasAttachments bool   `json:"has_attachments"`
	Snippet        string `json:"snippet"`
}

// ThreadParticipant is one external address in the conversation. Owner
// identities of every picked store are excluded, exactly as the world
// model's thread summaries do.
type ThreadParticipant struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	Messages int    `json:"messages"`
	LastAt   string `json:"last_at"`
}

// ThreadMessagesResult is one page of a conversation plus the conversation
// facts around it. Messages is chronological (oldest first); Count, FirstAt
// and LastAt cover every owner-visible message in the conversation, not
// only the returned page.
type ThreadMessagesResult struct {
	ConversationID string              `json:"conversation_id"`
	Subject        string              `json:"subject"`
	Messages       []ThreadMessage     `json:"messages"`
	Participants   []ThreadParticipant `json:"participants"`
	FirstAt        string              `json:"first_at"`
	LastAt         string              `json:"last_at"`
	Count          int                 `json:"count"`
	Truncated      bool                `json:"truncated"`
	// NewestMessageID is the owner-visible newest message of the whole
	// conversation, even when the page is anchored at an older message.
	NewestMessageID int64 `json:"newest_message_id"`
	// Newer counts owner-visible messages after an inclusive anchor. It is
	// zero for a latest page and for an exclusive older-page read.
	Newer int `json:"newer"`
}

// threadFromSQL is the one join of every conversation read. The scope
// verdict is an inner join: a message the owner population does not carry
// is never a member of this view.
const threadFromSQL = ` FROM messages m
	JOIN folders f ON f.id = m.folder_id
	JOIN scope_verdicts v ON v.message_id = m.id`

// ThreadMessages reads one conversation as the owner may see it: visible
// folders (active and picked), owner scope verdicts, and no drafts,
// deleted or junk rows. Messages are ordered oldest to newest; when the
// page limit cuts the history, the latest limit messages are returned but
// stay chronological and Truncated is true. beforeID pages older messages
// exclusively; zero starts at the latest message. The cursor is a row id,
// but paging follows the mail instant, not the insertion id, because a
// source may sync newest first and hand older mail larger row ids.
//
// The body is read only as a bounded prefix and returned only as a
// collapsed snippet. Shared/delegate replies stay outside this read: their
// effect reaches the API only through ThreadStates, as the state and its
// visibility, never as a subject or body.
func (s *Store) ThreadMessages(ctx context.Context, conversationID string, limit int, beforeID int64) (ThreadMessagesResult, error) {
	return s.threadMessages(ctx, conversationID, limit, beforeID, 0)
}

// ThreadMessagesAt reads the page ending at one owner-visible message,
// inclusive, so opening an older mail in a long conversation still shows
// that mail and the messages before it. Newer messages are outside this
// page; Truncated still reports whether older messages remain. atID and
// beforeID are mutually exclusive.
func (s *Store) ThreadMessagesAt(ctx context.Context, conversationID string, limit int, atID int64) (ThreadMessagesResult, error) {
	if atID <= 0 {
		return ThreadMessagesResult{ConversationID: conversationID, Messages: []ThreadMessage{}, Participants: []ThreadParticipant{}},
			fmt.Errorf("store: thread anchor id must be positive, got %d", atID)
	}
	return s.threadMessages(ctx, conversationID, limit, 0, atID)
}

func (s *Store) threadMessages(ctx context.Context, conversationID string, limit int, beforeID, atID int64) (ThreadMessagesResult, error) {
	out := ThreadMessagesResult{ConversationID: conversationID, Messages: []ThreadMessage{}, Participants: []ThreadParticipant{}}
	if err := ValidateConversationID(conversationID); err != nil {
		return out, err
	}
	if limit < 1 || limit > MaxThreadMessages {
		return out, fmt.Errorf("store: thread message limit must be between 1 and %d, got %d", MaxThreadMessages, limit)
	}
	if beforeID < 0 {
		return out, fmt.Errorf("store: thread before id must not be negative, got %d", beforeID)
	}
	if atID < 0 {
		return out, fmt.Errorf("store: thread anchor id must not be negative, got %d", atID)
	}
	if beforeID > 0 && atID > 0 {
		return out, errors.New("store: thread before id and anchor id are mutually exclusive")
	}
	vis, err := s.visible(ctx)
	if err != nil {
		return out, err
	}
	where := ownerFacingWhere(vis) + ` AND m.conversation_id = ?`

	count, firstAt, lastAt, err := s.threadSummary(ctx, where, conversationID)
	if err != nil {
		return out, err
	}
	if count == 0 {
		return out, fmt.Errorf("%w: %q", ErrThreadNotFound, conversationID)
	}
	subject, newestID, err := s.threadSubject(ctx, where, conversationID)
	if err != nil {
		return out, err
	}
	messages, truncated, newer, err := s.threadPage(ctx, where, conversationID, limit, beforeID, atID)
	if err != nil {
		return out, err
	}
	owner, err := s.OwnerSet(ctx)
	if err != nil {
		return out, err
	}
	participants, err := s.threadParticipants(ctx, where, conversationID, owner)
	if err != nil {
		return out, err
	}
	out.Subject = subject
	out.Messages = messages
	out.Participants = participants
	out.FirstAt, out.LastAt = firstAt, lastAt
	out.Count, out.Truncated = count, truncated
	out.NewestMessageID, out.Newer = newestID, newer
	return out, nil
}

// threadSummary returns the count and the dated span of every owner-visible
// message in the conversation. An absent instant is ignored rather than
// reported as an empty first/last.
func (s *Store) threadSummary(ctx context.Context, where, conversationID string) (count int, firstAt, lastAt string, err error) {
	var first, last sql.NullString
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*),
		MIN(NULLIF(`+instantSQL+`, '')), MAX(NULLIF(`+instantSQL+`, ''))`+threadFromSQL+`
		WHERE `+where, conversationID).Scan(&count, &first, &last)
	if err != nil {
		return 0, "", "", fmt.Errorf("store: thread summary: %w", err)
	}
	return count, first.String, last.String, nil
}

// threadSubject is the subject and owner-visible row id of the newest
// message. A draft, deleted, junk or shared row can never become the thread
// subject or the newest link target.
func (s *Store) threadSubject(ctx context.Context, where, conversationID string) (string, int64, error) {
	var subject string
	var messageID int64
	err := s.db.QueryRowContext(ctx, `SELECT m.id, m.subject`+threadFromSQL+`
		WHERE `+where+`
		ORDER BY `+instantSortSQL+` DESC, m.id DESC LIMIT 1`, conversationID).Scan(&messageID, &subject)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, fmt.Errorf("%w: %q", ErrThreadNotFound, conversationID)
	}
	if err != nil {
		return "", 0, fmt.Errorf("store: thread subject: %w", err)
	}
	return subject, messageID, nil
}

// threadPage reads the requested page newest-first so LIMIT keeps the
// requested end of the conversation, then reverses it into chronological
// order. One extra row is fetched to report whether older messages remain.
// A cursor is resolved to its mail instant inside this conversation, so the
// page filter and the ORDER BY use one sort key even when row ids are not
// chronological. beforeID is exclusive; atID is inclusive. newer counts the
// messages after an inclusive anchor and stays zero for an exclusive page.
func (s *Store) threadPage(ctx context.Context, where, conversationID string, limit int, beforeID, atID int64) ([]ThreadMessage, bool, int, error) {
	q := `SELECT m.id, m.conversation_id, m.subject, m.sender_name, m.sender_address,
		` + instantSQL + `, f.role, m.is_read, m.has_attachments,
		substr(m.body, 1, ` + strconv.Itoa(bodyPrefix) + `)` + threadFromSQL + `
		WHERE ` + where
	args := []any{conversationID}
	cursorID, inclusive := beforeID, false
	if atID > 0 {
		cursorID, inclusive = atID, true
	}
	newer := 0
	if cursorID > 0 {
		var cursor string
		err := s.db.QueryRowContext(ctx, `SELECT `+instantSortSQL+threadFromSQL+`
			WHERE `+where+` AND m.id = ?`, conversationID, cursorID).Scan(&cursor)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, 0, fmt.Errorf("%w: %d", ErrInvalidThreadCursor, cursorID)
		}
		if err != nil {
			return nil, false, 0, fmt.Errorf("store: thread cursor: %w", err)
		}
		if inclusive {
			q += ` AND (` + instantSortSQL + ` < ? OR (` + instantSortSQL + ` = ? AND m.id <= ?))`
			err = s.db.QueryRowContext(ctx, `SELECT COUNT(*)`+threadFromSQL+`
				WHERE `+where+` AND (`+instantSortSQL+` > ? OR (`+instantSortSQL+` = ? AND m.id > ?))`,
				conversationID, cursor, cursor, cursorID).Scan(&newer)
			if err != nil {
				return nil, false, 0, fmt.Errorf("store: thread newer count: %w", err)
			}
		} else {
			q += ` AND (` + instantSortSQL + ` < ? OR (` + instantSortSQL + ` = ? AND m.id < ?))`
		}
		args = append(args, cursor, cursor, cursorID)
	}
	q += ` ORDER BY ` + instantSortSQL + ` DESC, m.id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, false, 0, fmt.Errorf("store: thread page: %w", err)
	}
	defer rows.Close()
	page := make([]ThreadMessage, 0, limit)
	for rows.Next() {
		var m ThreadMessage
		var role, body string
		var isRead, hasAttachments int
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.Subject, &m.SenderName, &m.SenderAddress,
			&m.ReceivedAt, &role, &isRead, &hasAttachments, &body); err != nil {
			return nil, false, 0, fmt.Errorf("store: thread page row: %w", err)
		}
		m.Sent = mail.FolderRole(role) == mail.RoleSent
		m.IsRead, m.HasAttachments = isRead != 0, hasAttachments != 0
		m.Snippet = threadSnippet(body)
		page = append(page, m)
	}
	if err := rows.Err(); err != nil {
		return nil, false, 0, fmt.Errorf("store: thread page rows: %w", err)
	}
	truncated := len(page) > limit
	if truncated {
		page = page[:limit]
	}
	for i, j := 0, len(page)-1; i < j; i, j = i+1, j-1 {
		page[i], page[j] = page[j], page[i]
	}
	return page, truncated, newer, nil
}

// threadParticipant is the accumulator behind ThreadParticipant. lastTime
// orders the result without re-parsing the emitted timestamp.
type threadParticipant struct {
	name     string
	address  string
	messages int
	lastAt   string
	lastTime time.Time
}

// participantAccumulator is the one per-message participant rule of the
// store: owner identities are excluded, an address counts once per message,
// and last_at follows the mail instant. ThreadMessages and the conversation
// digest batch feed it different rows, but name, count and last_at cannot
// drift between them.
type participantAccumulator struct {
	byAddress map[string]*threadParticipant
	owner     map[string]bool
}

func newParticipantAccumulator(owner map[string]bool) *participantAccumulator {
	return &participantAccumulator{byAddress: map[string]*threadParticipant{}, owner: owner}
}

// externalAddress reports whether one normalized address is a participant of
// the owner's mail: present and not one of the owner's own identities. It is
// the one rule every participant projection applies.
func externalAddress(normalized string, owner map[string]bool) bool {
	return normalized != "" && !owner[normalized]
}

// externalAddresses returns the external addresses of one message: its sender
// and its recipients, each once, owner identities and empty addresses
// excluded. The thread participant list, the agenda's thread summaries and its
// deadlines all read this, so the same message cannot name one participant set
// in one view and a different one in another.
func externalAddresses(sender string, recipients []mail.Recipient, owner map[string]bool) map[string]bool {
	set := map[string]bool{}
	if address := contacts.Normalize(sender); externalAddress(address, owner) {
		set[address] = true
	}
	for _, r := range recipients {
		if address := contacts.Normalize(r.Address); externalAddress(address, owner) {
			set[address] = true
		}
	}
	return set
}

// add records one address of one message. seen is the per-message set, not
// the accumulator's lifetime set.
func (a *participantAccumulator) add(name, address, instant string, seen map[string]bool) {
	norm := contacts.Normalize(address)
	if !externalAddress(norm, a.owner) || seen[norm] {
		return
	}
	seen[norm] = true
	p := a.byAddress[norm]
	if p == nil {
		p = &threadParticipant{address: norm}
		a.byAddress[norm] = p
	}
	p.messages++
	if p.name == "" {
		p.name = strings.TrimSpace(name)
	}
	if instant != "" {
		if t, ok := mail.Parse(instant); ok {
			if p.lastAt == "" || t.After(p.lastTime) {
				p.lastAt, p.lastTime = instant, t
			}
		} else if p.lastAt == "" {
			p.lastAt = instant
		}
	}
}

func (a *participantAccumulator) list() []*threadParticipant {
	list := make([]*threadParticipant, 0, len(a.byAddress))
	for _, p := range a.byAddress {
		list = append(list, p)
	}
	return list
}

// threadParticipants aggregates every external address across every
// owner-visible message of the conversation: sender and recipients count
// once per message, owner identities are excluded, and a participant's
// last_at uses the same mail instant as the messages.
func (s *Store) threadParticipants(ctx context.Context, where, conversationID string, owner map[string]bool) ([]ThreadParticipant, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT m.sender_name, m.sender_address, `+instantSQL+`, `+recipientsSQL+`
		`+threadFromSQL+` WHERE `+where+` ORDER BY m.id`, conversationID)
	if err != nil {
		return nil, fmt.Errorf("store: thread participants: %w", err)
	}
	defer rows.Close()
	acc := newParticipantAccumulator(owner)
	for rows.Next() {
		var name, address, instant, recips string
		if err := rows.Scan(&name, &address, &instant, &recips); err != nil {
			return nil, fmt.Errorf("store: thread participant row: %w", err)
		}
		seen := map[string]bool{}
		acc.add(name, address, instant, seen)
		parsed, err := parseRecipients(recips)
		if err != nil {
			return nil, err
		}
		for _, r := range parsed {
			acc.add(r.Name, r.Address, instant, seen)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: thread participant rows: %w", err)
	}
	list := acc.list()
	sort.Slice(list, func(i, j int) bool {
		if !list[i].lastTime.Equal(list[j].lastTime) {
			return list[i].lastTime.After(list[j].lastTime)
		}
		if list[i].lastAt != list[j].lastAt {
			return list[i].lastAt > list[j].lastAt
		}
		return list[i].address < list[j].address
	})
	out := make([]ThreadParticipant, 0, len(list))
	for _, p := range list {
		out = append(out, ThreadParticipant{Name: p.name, Address: p.address, Messages: p.messages, LastAt: p.lastAt})
	}
	return out, nil
}

// threadSnippet collapses whitespace and cuts plain text to
// threadSnippetRunes. The input is always the bounded SQL prefix, never the
// stored full body or HTML.
func threadSnippet(body string) string {
	flat := strings.Join(strings.Fields(body), " ")
	runes := []rune(flat)
	if len(runes) > threadSnippetRunes {
		runes = runes[:threadSnippetRunes]
	}
	return string(runes)
}
