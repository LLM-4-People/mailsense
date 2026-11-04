package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// Exact address scope. A resolved person is a set of normalized addresses,
// never a substring of a display name: "Nadia" may name several people, while
// nadia@example.com is one mailbox address. The same IN clause and the same
// exact-counterpart rule serve every plan kind so a message, contact, company
// or latest-thread read cannot drift.

// normalizeScopeAddresses trims, lowercases and de-duplicates an address
// scope. It never invents an address.
func normalizeScopeAddresses(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, address := range in {
		address = mail.NormalizeAddress(address)
		if address == "" || seen[address] {
			continue
		}
		seen[address] = true
		out = append(out, address)
	}
	return out
}

// Normalized address expressions. The recipient address_norm and the sender
// sender_norm/sender_domain are written by the message writer, so the sender
// predicates can use their indexes; a recipient row can predate its column or
// be empty, and the raw address is then the fallback. The owner-identity
// column is normalized on read. One spelling per fact: a COALESCE over a NOT
// NULL column that forgot the empty-string fallback already existed in one
// query.
const (
	recipientNormSQL    = `COALESCE(NULLIF(r.address_norm, ''), mailsense_lower(r.address))`
	senderNormSQL       = `m.sender_norm`
	identityNormSQL     = `mailsense_lower(oi.address)`
	representingNormSQL = `mailsense_lower(m.sent_representing_address)`
)

// addressInSQL renders one parameterized IN clause over an already normalized
// address list.
func addressInSQL(column string, addresses []string) (string, []any) {
	if len(addresses) == 0 {
		return "", nil
	}
	placeholders := make([]string, len(addresses))
	args := make([]any, len(addresses))
	for i, address := range addresses {
		placeholders[i] = "?"
		args[i] = address
	}
	return column + " IN (" + strings.Join(placeholders, ", ") + ")", args
}

// exactCounterpartWhere matches one message by exact counterpart address: a
// sent item by a To/Cc recipient, a received item by its sender. It is the one
// predicate behind queryMessages and queryTime. LatestThreadForAddresses
// applies the same rule split by direction, because the OR form cannot follow
// either branch's index to its LIMIT 1.
func exactCounterpartWhere(addresses []string) (string, []any) {
	addresses = normalizeScopeAddresses(addresses)
	if len(addresses) == 0 {
		return "", nil
	}
	recipientIn, recipientArgs := addressInSQL(
		recipientNormSQL, addresses)
	senderIn, senderArgs := addressInSQL(senderNormSQL, addresses)
	where := `((` + sentByOwnerWhere + ` AND EXISTS (
		SELECT 1 FROM recipients r WHERE r.message_id = m.id AND r.kind IN ('to', 'cc') AND ` + recipientIn + `
	)) OR (` + notSentByOwnerWhere + ` AND ` + senderIn + `))`
	return where, append(recipientArgs, senderArgs...)
}

// latestThread is one candidate of the latest-thread read: the newest
// message of one branch and the conversation it belongs to.
type latestThread struct {
	conversationID string
	messageID      int64
	instant        string
}

// newerThan is the one tie-break of the latest-thread read and the same
// order both branches read in: instant descending, then message id
// descending. The branches are disjoint (sent_by_owner versus every other
// reason), so equal instants can only differ by message id.
func (a latestThread) newerThan(b latestThread) bool {
	if a.instant != b.instant {
		return a.instant > b.instant
	}
	return a.messageID > b.messageID
}

// latestReceivedQuery is the received half of the latest-thread read. It
// compares the stored sender key, so the exact address is one range read on
// messages_sender_norm and the newest row is the first row of that range.
func latestReceivedQuery(vis string) string {
	return `SELECT m.conversation_id, m.id, ` + instantSQL + `
		FROM messages m INDEXED BY messages_sender_norm
		JOIN folders f ON f.id = m.folder_id
		JOIN scope_verdicts v ON v.message_id = m.id
		WHERE ` + ownerFacingWhere(vis) + `
			AND ` + notSentByOwnerWhere + ` AND m.sender_norm = ?
			AND m.conversation_id <> ''
		ORDER BY ` + instantSQL + ` DESC, m.id DESC LIMIT 1`
}

// latestSentQuery is the sent half of the latest-thread read. The exact
// scoped address is looked up in recipients_address, then the matching
// messages are read on messages_instant, so the newest one is the first row
// of the page order and no branch sorts the owner population. The owner's
// outbox is not sorted by recipient, so the instant index is the only order
// that keeps LIMIT 1 bounded.
func latestSentQuery(vis string) string {
	return `SELECT m.conversation_id, m.id, ` + instantSQL + `
		FROM messages m INDEXED BY messages_instant
		JOIN folders f ON f.id = m.folder_id
		JOIN scope_verdicts v ON v.message_id = m.id
		WHERE ` + ownerFacingWhere(vis) + `
			AND ` + sentByOwnerWhere + `
			AND m.id IN (SELECT r.message_id FROM recipients r INDEXED BY recipients_address
				WHERE r.kind IN ('` + string(mail.To) + `', '` + string(mail.Cc) + `')
				AND (r.address_norm IN ` + stringSetSQL + `
					OR (r.address_norm = '' AND mailsense_lower(r.address) IN ` + stringSetSQL + `)))
			AND m.conversation_id <> ''
		ORDER BY ` + instantSQL + ` DESC, m.id DESC LIMIT 1`
}

// LatestThreadForAddresses returns the newest owner-visible conversation that
// contains one of the exact addresses, plus that message's row id. It is the
// one bounded read behind a resolved-person latest-exchange summary; a
// conversation with no owner-facing message is never returned.
//
// The counterpart predicate is split by direction: an OR over the recipient
// and sender halves drove the scope verdicts and sorted the owner population,
// while each half alone is an index range with the page order. The received
// half is one equality probe per address (an IN list would sort the ranges
// back); the sent half is one recipient lookup feeding the instant-ordered
// message page. The newer branch wins under latestThread.newerThan.
func (s *Store) LatestThreadForAddresses(ctx context.Context, addresses []string) (string, int64, error) {
	addresses = normalizeScopeAddresses(addresses)
	if len(addresses) == 0 {
		return "", 0, nil
	}
	vis, err := s.visible(ctx)
	if err != nil {
		return "", 0, err
	}
	listed := jsonList(addresses)
	var best latestThread
	for _, address := range addresses {
		var hit latestThread
		err := s.db.QueryRowContext(ctx, latestReceivedQuery(vis), address).
			Scan(&hit.conversationID, &hit.messageID, &hit.instant)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			continue
		case err != nil:
			return "", 0, fmt.Errorf("store: latest received conversation: %w", err)
		}
		if hit.newerThan(best) {
			best = hit
		}
	}
	var sent latestThread
	err = s.db.QueryRowContext(ctx, latestSentQuery(vis), listed, listed).
		Scan(&sent.conversationID, &sent.messageID, &sent.instant)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return "", 0, fmt.Errorf("store: latest sent conversation: %w", err)
	default:
		if sent.newerThan(best) {
			best = sent
		}
	}
	return best.conversationID, best.messageID, nil
}
