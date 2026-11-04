package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// ConversationDigest is one owner-facing conversation row: the latest
// message's metadata, the conversation's owner-visible counts, its open due
// date and the folded text the mailbox searches. Signals, topics and
// commitments are deliberately not part of Phase A.
type ConversationDigest struct {
	ConversationID string
	Subject        string
	SenderName     string
	SenderAddress  string
	// SearchText is folded subject + sender name + sender address. The
	// service searches this one value; it never re-reads message text.
	SearchText    string
	LastMessageID int64
	LastRevision  int64
	// CounterpartyAddress is the sender of the most recent received message.
	// A conversation the owner answered last still names an external
	// organization through it; it is empty when the conversation has no
	// received mail.
	CounterpartyAddress string
	// LastAt is the selected latest message's mail instant ("" when
	// undated). FirstAt is the oldest dated instant in the conversation.
	LastAt  string
	FirstAt string
	// DueDate is YYYY-MM-DD of the selected message's task due date, and
	// only while the task is not complete.
	DueDate            string
	Messages           int
	Sent               int
	Received           int
	Unread             int
	LastSent           bool
	LastIsRead         bool
	LastHasAttachments bool
}

// ConversationCoverage reports how much owner-visible mail the digest view
// covers. Conversations counts threaded conversations; Unthreaded counts the
// messages with no conversation id, which the digest rows intentionally omit.
type ConversationCoverage struct {
	Messages      int
	Conversations int
	Unthreaded    int
}

// ConversationDigestSet is the whole mailbox digest plus its coverage.
type ConversationDigestSet struct {
	Conversations []ConversationDigest
	Coverage      ConversationCoverage
}

// ConversationParticipants is one conversation's sorted external
// participants and the total unique count before the per-page cap.
type ConversationParticipants struct {
	Participants []ThreadParticipant
	Count        int
}

// conversationDigestQuery is the one whole-mailbox digest read: a single
// index-ordered stream over the conversation index. The per-conversation
// aggregate, latest member and latest received sender are folded in Go, so
// the statement never materializes the mailbox nor sorts it in a temporary
// B-tree. The result is ordered by conversation, then newest first, which is
// exactly messages_conversation_instant's key order; the caller restores the
// page order (latest first) after folding. It is a function so the plan test
// pins the access path.
func conversationDigestQuery(where string) string {
	return `SELECT m.id, m.conversation_id, m.subject, m.sender_name, m.sender_address,
			m.revision, m.is_read, m.has_attachments, m.task_due_date, m.flag_status,
			f.role, ` + instantSQL + `
		FROM messages m INDEXED BY messages_conversation_instant
		JOIN folders f ON f.id = m.folder_id
		JOIN scope_verdicts v ON v.message_id = m.id
		WHERE ` + where + ` AND m.conversation_id <> ''
		ORDER BY m.conversation_id, ` + instantSQL + ` DESC, m.id DESC`
}

// ConversationDigests returns one row per owner-visible conversation. The
// latest member is chosen by mail instant, not row id, because an initial
// sync may hand newer mail smaller ids. Unthreaded messages are excluded from
// the rows and reported by Coverage. The result is deterministic: latest
// first (undated last), conversation id descending.
func (s *Store) ConversationDigests(ctx context.Context) (ConversationDigestSet, error) {
	out := ConversationDigestSet{Conversations: []ConversationDigest{}}
	vis, err := s.visible(ctx)
	if err != nil {
		return out, err
	}
	where := ownerFacingWhere(vis)
	rows, err := s.db.QueryContext(ctx, conversationDigestQuery(where))
	if err != nil {
		return out, fmt.Errorf("store: conversation digests: %w", err)
	}
	defer rows.Close()

	var current string
	var digest ConversationDigest
	var firstAt, latestReceived string
	var hasLatestReceived bool
	flush := func() {
		if current == "" {
			return
		}
		digest.FirstAt = firstAt
		digest.Received = digest.Messages - digest.Sent
		digest.CounterpartyAddress = latestReceived
		digest.SearchText = conversationSearchText(digest.Subject, digest.SenderName, digest.SenderAddress)
		out.Conversations = append(out.Conversations, digest)
	}
	for rows.Next() {
		var conv, subject, senderName, senderAddress, instant, role, taskDue string
		var id, revision int64
		var isRead, hasAttachments int
		var flagStatus sql.NullInt64
		if err := rows.Scan(&id, &conv, &subject, &senderName, &senderAddress,
			&revision, &isRead, &hasAttachments, &taskDue, &flagStatus, &role, &instant); err != nil {
			return out, fmt.Errorf("store: conversation digest row: %w", err)
		}
		if conv != current {
			flush()
			current = conv
			digest = ConversationDigest{
				ConversationID:     conv,
				Subject:            subject,
				SenderName:         senderName,
				SenderAddress:      senderAddress,
				LastMessageID:      id,
				LastRevision:       revision,
				LastAt:             instant,
				DueDate:            conversationDueDate(taskDue, flagStatus),
				LastSent:           mail.FolderRole(role) == mail.RoleSent,
				LastIsRead:         isRead != 0,
				LastHasAttachments: hasAttachments != 0,
			}
			firstAt, latestReceived, hasLatestReceived = "", "", false
		}
		digest.Messages++
		if mail.FolderRole(role) == mail.RoleSent {
			digest.Sent++
		} else {
			if !hasLatestReceived {
				latestReceived, hasLatestReceived = senderAddress, true
			}
			if isRead == 0 {
				digest.Unread++
			}
		}
		if instant != "" && (firstAt == "" || instant < firstAt) {
			firstAt = instant
		}
	}
	if err := rows.Err(); err != nil {
		return out, fmt.Errorf("store: conversation digest rows: %w", err)
	}
	flush()
	if err := rows.Close(); err != nil {
		return out, fmt.Errorf("store: conversation digest rows: %w", err)
	}
	// The stream follows the conversation index; the page order is latest
	// first (undated last), conversation id descending.
	sort.Slice(out.Conversations, func(i, j int) bool {
		if out.Conversations[i].LastAt != out.Conversations[j].LastAt {
			return out.Conversations[i].LastAt > out.Conversations[j].LastAt
		}
		return out.Conversations[i].ConversationID > out.Conversations[j].ConversationID
	})
	// Coverage comes from the digest aggregate plus one small count of the
	// unthreaded messages, not from a second walk over the whole mailbox.
	coverage := ConversationCoverage{Conversations: len(out.Conversations)}
	for _, d := range out.Conversations {
		coverage.Messages += d.Messages
	}
	unthreaded, err := s.unthreadedMessages(ctx, where)
	if err != nil {
		return out, err
	}
	coverage.Unthreaded = unthreaded
	coverage.Messages += unthreaded
	out.Coverage = coverage
	return out, nil
}

// conversationDueDate is the digest's due-date projection for the selected
// latest member: its task due date while the flag is not complete, bounded to
// the date part. It is the Go form of the former SQL CASE, kept beside the
// row fold so the wire value cannot drift.
func conversationDueDate(taskDue string, flagStatus sql.NullInt64) string {
	if flagStatus.Valid && flagStatus.Int64 == 1 {
		return ""
	}
	if taskDue == "" {
		return ""
	}
	if runes := []rune(taskDue); len(runes) > 10 {
		taskDue = string(runes[:10])
	}
	return taskDue
}

// unthreadedMessages counts the owner-visible messages outside a
// conversation under the digest predicate. The conversation index narrows
// the scan to the empty conversation id instead of walking every message.
func (s *Store) unthreadedMessages(ctx context.Context, where string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM messages m INDEXED BY messages_conversation
		JOIN folders f ON f.id = m.folder_id
		JOIN scope_verdicts v ON v.message_id = m.id
		WHERE m.conversation_id = '' AND `+where).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: unthreaded count: %w", err)
	}
	return n, nil
}

// ConversationParticipants aggregates the requested conversations in one
// batch: the sender of received mail and the To/Cc recipients of sent mail,
// owner identities excluded, one count per address per message, last_at by
// the message's mail instant. Each list is sorted by messages, then last_at,
// then address and capped at per; Count is the total unique count.
func (s *Store) ConversationParticipants(ctx context.Context, conversationIDs []string, per int) (map[string]ConversationParticipants, error) {
	out := map[string]ConversationParticipants{}
	if len(conversationIDs) == 0 {
		return out, nil
	}
	if per < 1 {
		return nil, fmt.Errorf("store: conversation participant cap must be positive, got %d", per)
	}
	ids := make([]string, 0, len(conversationIDs))
	seen := map[string]bool{}
	for _, id := range conversationIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return out, nil
	}
	vis, err := s.visible(ctx)
	if err != nil {
		return nil, err
	}
	owner, err := s.OwnerSet(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT m.conversation_id, m.sender_name, m.sender_address,
			`+instantSQL+`, f.role, `+recipientsSQL+`
		-- The page asks for at most 100 conversations. Without statistics
		-- SQLite starts from scope_verdicts and walks every owner message;
		-- the conversation index is the one bound that keeps this read
		-- proportional to the page.
		FROM messages m INDEXED BY messages_conversation
		JOIN folders f ON f.id = m.folder_id
		JOIN scope_verdicts v ON v.message_id = m.id
		WHERE `+ownerFacingWhere(vis)+` AND m.conversation_id IN `+stringSetSQL+`
		ORDER BY m.conversation_id, m.id`, jsonList(ids))
	if err != nil {
		return nil, fmt.Errorf("store: conversation participants: %w", err)
	}
	defer rows.Close()
	accumulators := map[string]*participantAccumulator{}
	for rows.Next() {
		var conv, name, address, instant, role, recips string
		if err := rows.Scan(&conv, &name, &address, &instant, &role, &recips); err != nil {
			return nil, fmt.Errorf("store: conversation participant row: %w", err)
		}
		acc := accumulators[conv]
		if acc == nil {
			acc = newParticipantAccumulator(owner)
			accumulators[conv] = acc
		}
		seenAddresses := map[string]bool{}
		if mail.FolderRole(role) == mail.RoleSent {
			parsed, err := parseRecipients(recips)
			if err != nil {
				return nil, err
			}
			for _, r := range parsed {
				if r.Kind != mail.To && r.Kind != mail.Cc {
					continue
				}
				acc.add(r.Name, r.Address, instant, seenAddresses)
			}
			continue
		}
		acc.add(name, address, instant, seenAddresses)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: conversation participant rows: %w", err)
	}
	for conv, acc := range accumulators {
		list := acc.list()
		sort.Slice(list, func(i, j int) bool {
			if list[i].messages != list[j].messages {
				return list[i].messages > list[j].messages
			}
			if !list[i].lastTime.Equal(list[j].lastTime) {
				return list[i].lastTime.After(list[j].lastTime)
			}
			if list[i].lastAt != list[j].lastAt {
				return list[i].lastAt > list[j].lastAt
			}
			return list[i].address < list[j].address
		})
		capN := min(per, len(list))
		participants := make([]ThreadParticipant, 0, capN)
		for _, p := range list[:capN] {
			participants = append(participants, ThreadParticipant{
				Name: p.name, Address: p.address, Messages: p.messages, LastAt: p.lastAt,
			})
		}
		out[conv] = ConversationParticipants{Participants: participants, Count: len(list)}
	}
	return out, nil
}

// ConversationSnippets reads bounded, collapsed snippets for the requested
// latest message ids in one query. The full body never leaves the store, and
// the owner-content predicate keeps a shared/delegate row out even when a
// caller hands in its id.
func (s *Store) ConversationSnippets(ctx context.Context, messageIDs []int64) (map[int64]string, error) {
	out := map[int64]string{}
	if len(messageIDs) == 0 {
		return out, nil
	}
	vis, err := s.visible(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT m.id, substr(m.body, 1, `+strconv.Itoa(bodyPrefix)+`)
		FROM messages m
		JOIN folders f ON f.id = m.folder_id
		JOIN scope_verdicts v ON v.message_id = m.id
		WHERE `+ownerFacingWhere(vis)+` AND m.id IN `+idSetSQL, idSet(messageIDs))
	if err != nil {
		return nil, fmt.Errorf("store: conversation snippets: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var body string
		if err := rows.Scan(&id, &body); err != nil {
			return nil, fmt.Errorf("store: conversation snippet row: %w", err)
		}
		out[id] = threadSnippet(body)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: conversation snippet rows: %w", err)
	}
	return out, nil
}

// conversationSearchText is the exact folded string the service searches. It
// lives beside the digest row so creation and search cannot disagree on the
// separators or the order of the parts.
func conversationSearchText(subject, senderName, senderAddress string) string {
	return mail.Fold(strings.Join([]string{subject, senderName, senderAddress}, "\n"))
}
