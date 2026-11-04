package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
)

// ThreadStatesBatch bounds one conversation-state read: how many conversations
// ThreadStates resolves at once. Phase 2 fetches at most one frontier row per
// conversation per round, so a single round reads at most this many
// body/header rows and only the current chunk's accumulated evidence stays
// referenced. The conversations reader chunks the ids it sends by the same
// bound, so the store read and the caller's request are one scale.
const ThreadStatesBatch = 500

// threadStateSelect is the evidence of one conversation member: every field
// the resolver reads plus the two derived facts it can use. ManyTo is the
// "more than one To recipient" decision, and the owner identity probe stands
// in for a missing scope verdict on a sent row. Both are computed only where
// they can change the resolver's answer: the probe is skipped for a row that
// already has a verdict or is not sent.
const threadStateSelect = `SELECT m.id, m.revision, m.conversation_id, m.sender_name, m.sender_address,
		m.addressed_to_me, f.role, f.store_kind, COALESCE(v.verdict, ''), COALESCE(v.reason, ''),
		` + instantSQL + `, m.message_class, m.in_reply_to,
		m.message_references, m.last_verb_executed, m.has_attachments,
		((SELECT COUNT(*) FROM recipients r WHERE r.message_id = m.id AND r.kind = 'to') > 1),
		CASE WHEN COALESCE(v.verdict, '') = '' AND f.role = 'sent' THEN
			EXISTS (SELECT 1 FROM owner_identities oi
				WHERE oi.source_id = m.source_id AND oi.store_id = m.store_id
					AND (` + identityNormSQL + ` = ` + senderNormSQL + `
						OR ` + identityNormSQL + ` = ` + representingNormSQL + `))
		ELSE 0 END`

// threadStateFrom is the evidence join of ThreadStates: visible mail of every
// store kind, including shared and delegate mail, with the scope verdict when
// one exists.
const threadStateFrom = `
		FROM messages m
		JOIN folders f ON f.id = m.folder_id
		LEFT JOIN scope_verdicts v ON v.message_id = m.id`

// threadStateOrder groups one conversation's members. The resolver re-sorts
// them by (instant, row id) itself, so ordering by the CASE instant expression
// here would only add a temporary B-tree over the body/header payload.
const threadStateOrder = `
		ORDER BY m.conversation_id, m.id`

// ThreadStates resolves every requested conversation from all visible
// messages in it, including shared-mailbox replies that owner-only reads
// exclude. Only the state is returned; no shared body or subject leaves
// this method. signals carries the classifier evidence for owner rows
// (shared rows have none): the directed reply answer, the wider question
// signal, the dominant ask and the subject path.
func (s *Store) ThreadStates(ctx context.Context, conversationIDs []string, signals map[int64]threadintel.Signal) (map[string]threadintel.Assessment, error) {
	return s.threadStates(ctx, conversationIDs, signals, ThreadStatesBatch, nil)
}

// threadStates is ThreadStates with an injectable evidence batch and an
// optional peak recorder. Production passes the package constant and nil;
// tests pass both to prove the bound and its behavioral parity.
func (s *Store) threadStates(ctx context.Context, conversationIDs []string, signals map[int64]threadintel.Signal, batch int, evidencePeak *int) (map[string]threadintel.Assessment, error) {
	ids := make([]string, 0, len(conversationIDs))
	seen := map[string]bool{}
	for _, id := range conversationIDs {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return map[string]threadintel.Assessment{}, nil
	}
	if batch <= 0 {
		return nil, fmt.Errorf("store: thread states: invalid evidence batch %d", batch)
	}
	vis, err := s.visible(ctx)
	if err != nil {
		return nil, err
	}
	visibility, err := s.threadVisibility(ctx)
	if err != nil {
		return nil, err
	}
	// Phase 1 and phase 2 must observe one snapshot: a sync between them
	// could otherwise pair a metadata row with a body from a later revision.
	return inReadTx(ctx, s.db, "thread states", func(tx *sql.Tx) (map[string]threadintel.Assessment, error) {
		rows, err := tx.QueryContext(ctx, threadStateSelect+threadStateFrom+`
		WHERE `+vis+`
			AND m.conversation_id IN `+stringSetSQL+`
			AND `+threadContentRoleWhere+threadStateOrder, jsonList(ids))
		if err != nil {
			return nil, fmt.Errorf("store: thread states: %w", err)
		}
		byConversation := make(map[string][]threadintel.Message, len(ids))
		for rows.Next() {
			m, err := scanThreadState(rows, signals)
			if err != nil {
				_ = rows.Close()
				return nil, err
			}
			if m.ConversationID == "" {
				continue
			}
			byConversation[m.ConversationID] = append(byConversation[m.ConversationID], m)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("store: thread state rows: %w", err)
		}
		_ = rows.Close()

		// The resolver reads body and headers only to reject auto-replies,
		// acknowledgements and empty notes while walking newest-first to the
		// first substantive message. Resolving a bounded conversation chunk at a
		// time keeps only that chunk's frontier evidence resident: the old loop
		// held every requested conversation's frontier at once, 10,352 rows on
		// the live mailbox. The read transaction stays open so every chunk sees
		// the phase-1 snapshot.
		conversations := make([]string, 0, len(byConversation))
		for _, id := range ids {
			if _, ok := byConversation[id]; ok {
				conversations = append(conversations, id)
			}
		}
		options := threadintel.Options{Visibility: visibility}
		out := make(map[string]threadintel.Assessment, len(conversations))
		for start := 0; start < len(conversations); start += batch {
			end := min(start+batch, len(conversations))
			chunk := make(map[string][]threadintel.Message, end-start)
			for _, id := range conversations[start:end] {
				chunk[id] = byConversation[id]
				delete(byConversation, id)
			}
			resolved, err := resolveThreadStateChunk(ctx, tx, chunk, options, evidencePeak)
			if err != nil {
				return nil, err
			}
			for id, assessment := range resolved {
				out[id] = assessment
			}
			clear(chunk)
		}
		return out, nil
	})
}

// resolveThreadStateChunk resolves one bounded set of conversations from the
// phase-1 metadata. The frontier loop is unchanged: each round asks every
// unresolved conversation for its newest unloaded member, loads exactly those
// rows, and repeats once the resolver can reach a substantive message. The
// Thread objects (bodies, headers and their parsed evidence), the loaded sets
// and the evidence map are local to the chunk, so they become unreachable
// before the next chunk starts. evidencePeak, when non-nil, records the
// largest single frontier read; tests use it to prove the bound.
func resolveThreadStateChunk(ctx context.Context, tx *sql.Tx, byConversation map[string][]threadintel.Message, options threadintel.Options, evidencePeak *int) (map[string]threadintel.Assessment, error) {
	at := make(map[int64]threadLocation)
	// One Thread per conversation owns its ordered members and their parsed
	// evidence. Every frontier round and the final resolve share it, so a
	// member loaded once is parsed once instead of once per round.
	threads := make(map[string]*threadintel.Thread, len(byConversation))
	for id, messages := range byConversation {
		for i := range messages {
			at[messages[i].RowID] = threadLocation{conversation: id, index: i}
		}
		threads[id] = threadintel.NewThread(messages, threadintel.SharedCache())
	}
	loaded := make(map[string]map[int64]struct{}, len(byConversation))
	for id := range byConversation {
		loaded[id] = map[int64]struct{}{}
	}
	for {
		type frontier struct {
			conversation string
			rowID        int64
		}
		var need []frontier
		for id, thread := range threads {
			rowID, ok := thread.EvidenceFrontier(loaded[id], options)
			if ok {
				need = append(need, frontier{conversation: id, rowID: rowID})
			}
		}
		if len(need) == 0 {
			break
		}
		ids := make([]int64, len(need))
		for i, f := range need {
			ids[i] = f.rowID
		}
		evidence, err := loadThreadEvidence(ctx, tx, ids)
		if err != nil {
			return nil, err
		}
		if len(evidence) != len(ids) {
			return nil, fmt.Errorf("store: thread evidence: read %d of %d frontier rows", len(evidence), len(ids))
		}
		if evidencePeak != nil && len(evidence) > *evidencePeak {
			*evidencePeak = len(evidence)
		}
		for _, f := range need {
			loc, ok := at[f.rowID]
			if !ok || loc.conversation != f.conversation {
				return nil, fmt.Errorf("store: thread evidence: row %d is not in conversation %s", f.rowID, f.conversation)
			}
			e := evidence[f.rowID]
			threads[f.conversation].SetContent(loc.index, e.body, e.headers)
			loaded[f.conversation][f.rowID] = struct{}{}
		}
	}
	out := make(map[string]threadintel.Assessment, len(threads))
	for id, thread := range threads {
		out[id] = thread.Resolve(options)
	}
	return out, nil
}

// threadLocation points at one member in its conversation slice.
type threadLocation struct {
	conversation string
	index        int
}

// threadEvidence is the deferred half of a thread-state read.
type threadEvidence struct {
	body    string
	headers string
}

// loadThreadEvidence reads the bodies and headers of one frontier batch by
// row id. The ids come from the phase-1 snapshot, so no visibility predicate
// can change the set.
func loadThreadEvidence(ctx context.Context, q queryer, ids []int64) (map[int64]threadEvidence, error) {
	rows, err := q.QueryContext(ctx, `SELECT m.id, m.body, m.headers FROM messages m WHERE m.id IN `+idSetSQL, idSet(ids))
	if err != nil {
		return nil, fmt.Errorf("store: thread evidence: %w", err)
	}
	defer rows.Close()
	out := make(map[int64]threadEvidence, len(ids))
	for rows.Next() {
		var id int64
		var e threadEvidence
		if err := rows.Scan(&id, &e.body, &e.headers); err != nil {
			return nil, fmt.Errorf("store: thread evidence row: %w", err)
		}
		out[id] = e
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: thread evidence rows: %w", err)
	}
	return out, nil
}

// scanThreadState parses one row of threadStateSelect. The classifier
// signal is keyed by row id; only owner rows have one, shared rows have none.
func scanThreadState(rows *sql.Rows, signals map[int64]threadintel.Signal) (threadintel.Message, error) {
	var m threadintel.Message
	var role, storeKind, instant, verdict, reason string
	var lastVerb sql.NullInt64
	var manyTo, ownerSender int
	if err := rows.Scan(&m.RowID, &m.Revision, &m.ConversationID, &m.SenderName, &m.SenderAddress,
		&m.AddressedToMe, &role, &storeKind, &verdict, &reason, &instant,
		&m.MessageClass, &m.InReplyTo, &m.References, &lastVerb, &m.HasAttachments,
		&manyTo, &ownerSender); err != nil {
		return threadintel.Message{}, fmt.Errorf("store: thread state row: %w", err)
	}
	m.ManyTo = manyTo != 0
	m.Role = mail.FolderRole(role)
	m.Verdict, m.Reason = scope.Verdict(strings.TrimSpace(verdict)), scope.Reason(strings.TrimSpace(reason))
	if m.Verdict == "" && ownerSender != 0 && m.Role == mail.RoleSent {
		// Scope has not judged this copied/sent item yet, but the
		// store's own identity says the owner sent it.
		m.Verdict, m.Reason = scope.Owner, scope.ReasonSentByOwner
	}
	m.OtherOwner = !mail.StoreKind(storeKind).OwnerContent()
	m.OwnerFacing = m.Verdict == scope.Owner && !m.OtherOwner
	if lastVerb.Valid {
		m.LastVerbExecuted = int(lastVerb.Int64)
	}
	if t, ok := mail.Parse(instant); ok {
		m.At = t
	}
	if signal, ok := signals[m.RowID]; ok {
		m.ReplyNeeded, m.IsQuestion, m.AsksFor = signal.ReplyNeeded, signal.IsQuestion, signal.AsksFor
		m.Topic, m.Subtopic, m.Automated = signal.Topic, signal.Subtopic, signal.Automated
	}
	return m, nil
}

// threadVisibility is Complete only when every shared/delegate mailbox has
// an active and picked folder (visibleSQL). Otherwise it is Partial: a
// colleague's reply may exist outside the visible evidence. It never claims
// the reply does not exist.
func (s *Store) threadVisibility(ctx context.Context) (threadintel.Visibility, error) {
	var missing int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mailboxes mb
		WHERE mb.kind IN ('shared', 'delegate')
			AND NOT EXISTS (SELECT 1 FROM folders f WHERE f.store_key = mb.key AND `+visibleSQL+`)`).Scan(&missing)
	if err != nil {
		return threadintel.UnknownVisibility, fmt.Errorf("store: thread visibility: %w", err)
	}
	if missing > 0 {
		return threadintel.Partial, nil
	}
	return threadintel.Complete, nil
}
