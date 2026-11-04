package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/mail"
)

// Person candidates are the one bounded read behind Ask's entity resolver.
// A display name is not an identity: "Nadia" can be several people, one person
// can write from several addresses, and an address that only appears in the
// address book still names a person. The query merges visible owner mail
// participants (received sender, sent To/Cc) with the picked address books,
// excludes every owner address, and returns exact normalized addresses. It
// never reads a body and never calls a provider.

// PersonCandidate is one address observed in owner mail or the visible address
// book, with its best display name, organization when one is declared, message
// count and last activity. Address is always normalized.
type PersonCandidate struct {
	Address    string `json:"address"`
	Name       string `json:"name,omitempty"`
	Company    string `json:"company,omitempty"`
	Messages   int    `json:"messages"`
	LastAt     string `json:"last_at,omitempty"`
	Declared   bool   `json:"declared,omitempty"`
	NameCount  int    `json:"-"`
	ContactKey string `json:"-"`
}

const (
	// MaxPersonCandidateTerms bounds the question tokens one candidate read
	// accepts. A name or address needs far fewer; the bound keeps a malformed
	// direct call from building an unbounded OR expression.
	MaxPersonCandidateTerms = 8
	// MaxPersonCandidates bounds one candidate read. Clarification shows at
	// most six; the wider bound leaves room to rank and group addresses.
	MaxPersonCandidates = 200
)

// personCandidatesKey is the identity of one candidate read. The folded
// terms are already normalized and ordered by the caller's mention order.
type personCandidatesKey struct {
	generation uint64
	snap       *config.Snapshot
	terms      string
	limit      int
}

// maxCachedCandidateReads bounds the candidate cache; resetting it is cheaper
// than maintaining an LRU index and loses only a full-mailbox scan.
const maxCachedCandidateReads = 32

// PersonCandidates reads people matching at least one term in their display
// name or exact address. It is one query: no per-term round trip and no body
// scan. Owner identities, shared/delegate stores, drafts, deleted and junk
// mail are excluded exactly as every other owner-facing read does. A result
// is cached per mail generation: Ask repeats the same names while the mail
// has not moved.
func (s *Store) PersonCandidates(ctx context.Context, terms []string, limit int) ([]PersonCandidate, error) {
	terms = normalizeCandidateTerms(terms)
	if len(terms) == 0 {
		return []PersonCandidate{}, nil
	}
	if limit <= 0 {
		limit = MaxPersonCandidates
	}
	if limit > MaxPersonCandidates {
		limit = MaxPersonCandidates
	}
	termsKey, _ := json.Marshal(terms)
	snap := s.cfg.Snapshot()
	key := personCandidatesKey{generation: s.MailGeneration(), snap: snap, terms: string(termsKey), limit: limit}
	s.candMu.Lock()
	if rows, ok := s.cands[key]; ok {
		out := make([]PersonCandidate, len(rows))
		copy(out, rows)
		s.candMu.Unlock()
		return out, nil
	}
	s.candMu.Unlock()
	vis, err := s.visible(ctx)
	if err != nil {
		return nil, err
	}
	receivedFilter, receivedArgs := personTermFilter("m.sender_name", senderNormSQL, terms)
	recipientFilter, recipientArgs := personTermFilter("r.name",
		recipientNormSQL, terms)
	contactFilter, contactArgs := personTermFilter("c.display_name", "ce.address_norm", terms)

	sqlText := `WITH people AS (
		SELECT ` + senderNormSQL + ` AS address_norm, m.sender_name AS name, '' AS company,
			m.id AS message_id, ` + instantSQL + ` AS last_at, 1 AS is_mail, 0 AS is_contact, '' AS contact_key
		FROM messages m
		JOIN folders f ON f.id = m.folder_id
		JOIN scope_verdicts v ON v.message_id = m.id
		WHERE ` + ownerFacingWhere(vis) + `
			AND ` + notSentByOwnerWhere + ` AND trim(m.sender_address) <> '' AND ` + receivedFilter + `
		UNION ALL
		SELECT ` + recipientNormSQL + ` AS address_norm,
			r.name AS name, '' AS company, m.id AS message_id, ` + instantSQL + ` AS last_at, 1 AS is_mail, 0 AS is_contact,
			'' AS contact_key
		FROM messages m
		JOIN folders f ON f.id = m.folder_id
		JOIN scope_verdicts v ON v.message_id = m.id
		JOIN recipients r ON r.message_id = m.id AND r.kind IN ('to', 'cc')
		WHERE ` + ownerFacingWhere(vis) + `
			AND ` + sentByOwnerWhere + ` AND trim(r.address) <> '' AND ` + recipientFilter + `
		UNION ALL
		SELECT ce.address_norm AS address_norm, c.display_name AS name, c.company AS company,
			0 AS message_id, '' AS last_at, 0 AS is_mail, 1 AS is_contact,
			c.source_id || char(31) || c.store_id || char(31) || c.entry_id AS contact_key
		FROM contact_emails ce
		JOIN contacts c ON c.id = ce.contact_id
		WHERE c.store_key IN ` + stringSetSQL + ` AND ce.address_norm <> '' AND ` + contactFilter + `
	), matched AS (
		SELECT p.* FROM people p
		WHERE p.address_norm <> ''
			AND NOT EXISTS (
				SELECT 1 FROM owner_identities oi
				WHERE ` + identityNormSQL + ` = p.address_norm
			)
	), agg AS (
		SELECT address_norm, name, company,
			COUNT(DISTINCT CASE WHEN is_mail = 1 THEN message_id END) AS messages,
			MAX(CASE WHEN is_mail = 1 THEN last_at ELSE '' END) AS last_at,
			MAX(is_contact) AS declared,
			COUNT(*) AS name_rows,
			MAX(contact_key) AS contact_key
		FROM matched
		GROUP BY address_norm, name, company
	)
	SELECT address_norm, name, company, messages, last_at, declared, name_rows, contact_key
	FROM agg
	ORDER BY messages DESC, last_at DESC, address_norm ASC, name ASC
	LIMIT ?`

	args := make([]any, 0, 2*(len(receivedArgs)+len(recipientArgs)+len(contactArgs))+2)
	args = append(args, receivedArgs...)
	args = append(args, recipientArgs...)
	args = append(args, jsonList(s.pickedKeys()))
	args = append(args, contactArgs...)
	args = append(args, limit)

	out, err := query[PersonCandidate](ctx, s.db, sqlText, args, func(rows *sql.Rows, candidate *PersonCandidate) error {
		var declared int
		if err := rows.Scan(&candidate.Address, &candidate.Name, &candidate.Company,
			&candidate.Messages, &candidate.LastAt, &declared, &candidate.NameCount, &candidate.ContactKey); err != nil {
			return err
		}
		candidate.Declared = declared != 0
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: person candidates: %w", err)
	}
	if out == nil {
		out = make([]PersonCandidate, 0, limit)
	}
	// A declared owner address is configuration, not a row of
	// owner_identities, so the SQL exclusion cannot see it. The canonical
	// owner set is the one filter (subaddresses included).
	owners, err := s.owners(ctx, snap)
	if err != nil {
		return nil, err
	}
	out = filterOwnerCandidates(out, owners)
	s.candMu.Lock()
	if s.cands == nil || len(s.cands) >= maxCachedCandidateReads {
		s.cands = make(map[personCandidatesKey][]PersonCandidate, maxCachedCandidateReads)
	}
	s.cands[key] = append([]PersonCandidate(nil), out...)
	s.candMu.Unlock()
	return out, nil
}

// normalizeCandidateTerms trims, lowercases, de-duplicates and bounds the
// question terms one candidate read accepts.
func normalizeCandidateTerms(in []string) []string {
	out := make([]string, 0, min(len(in), MaxPersonCandidateTerms))
	seen := make(map[string]bool, len(in))
	for _, term := range in {
		// The term is the same folded form the haystack holds: the owner
		// types "simon" where the address book says "Simon", and the
		// two must match. Folding also keeps an exact address intact.
		term = mail.Fold(strings.TrimSpace(term))
		if term == "" || seen[term] {
			continue
		}
		seen[term] = true
		out = append(out, term)
		if len(out) == MaxPersonCandidateTerms {
			break
		}
	}
	return out
}

// filterOwnerCandidates drops every candidate an owner identity owns,
// including a declared address and a subaddress of one.
func filterOwnerCandidates(rows []PersonCandidate, owners map[string]Owner) []PersonCandidate {
	if len(rows) == 0 || len(owners) == 0 {
		return rows
	}
	out := rows[:0]
	for _, row := range rows {
		owned := false
		for _, owner := range owners {
			if owner.Identity.Owns(row.Address) {
				owned = true
				break
			}
		}
		if !owned {
			out = append(out, row)
		}
	}
	return out
}

// personTermHaystack renders a display name or an address as one folded,
// lowercased token string with spaces around every token. Folding is the same
// mail.Fold the question terms use, so "Simon" matches the typed
// "simon" instead of requiring the accent. A match is exactly " term ",
// never an inner substring: the question word "liste" does not select the
// sender "listening@lenovo.com", and it does not select
// "benoit@ivoire-finance.test" through its domain either.
func personTermHaystack(column string) string {
	return "(' ' || replace(replace(replace(replace(replace(replace(" + foldSQL + "(" + column +
		"), '@', ' '), '.', ' '), '-', ' '), '_', ' '), '''', ' '), ',', ' ') || ' ')"
}

// personTermFilter renders one whole-token predicate over a display name and
// an address. instr() keeps '%' and '_' in a question literal: a name is not a
// LIKE pattern.
func personTermFilter(nameColumn, addressColumn string, terms []string) (string, []any) {
	nameHaystack := personTermHaystack("COALESCE(" + nameColumn + ", '')")
	addressHaystack := personTermHaystack(addressColumn)
	clauses := make([]string, 0, len(terms))
	args := make([]any, 0, 2*len(terms))
	for _, term := range terms {
		clauses = append(clauses, "(instr("+nameHaystack+", ' ' || ? || ' ') > 0 OR instr("+addressHaystack+", ' ' || ? || ' ') > 0)")
		args = append(args, term, term)
	}
	return "(" + strings.Join(clauses, " OR ") + ")", args
}
