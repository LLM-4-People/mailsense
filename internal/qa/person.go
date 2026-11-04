package qa

import (
	"context"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mailquery"
	"github.com/LLM-4-People/mailsense/internal/search"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// A question about a person is not only about the address its search hits
// returned: the same person writes from several addresses, and their other
// organizations appear in the display name, the domain and the signature.
// This leg discovers every address that writes under the person's display
// name and supplies its newest received messages, so the planner can answer
// "the other company" from evidence instead of from a keyword table.

// Person-evidence bounds. The name search is bounded to the addresses a
// mailbox is likely to use for one person; two newest received messages per
// address carry a current signature, and six numbered messages keep the
// person's addresses visible without crowding the general hits out of
// qa.max_messages.
const (
	minPersonNameTokens = 2
	minPersonTokenRunes = 3
	personSearchLimit   = 20
	personRecentLimit   = 2
	personEvidenceLimit = 6
)

// personEvidenceBudget is how many numbered slots the person leg may take.
// The general hits are ranked by the question itself; the person leg is a
// supplement, so it never takes more than half the context (and at most six).
// A one-message context leaves the slot to the ranked hit.
func personEvidenceBudget(limit int) int {
	budget := limit / 2
	if budget > personEvidenceLimit {
		return personEvidenceLimit
	}
	return budget
}

// personName names the person a question asks about, from the display names
// of the retrieved hits. A display name is a candidate only when it looks
// like a person (at least a first and last token, no address, no role
// token), and the question must contain one of its substantial tokens
// (three runes or more, so initials and particles do not select a person).
// The candidate with the most matched tokens, then the most hits, wins; two
// distinct names tied at the top are ambiguous and return "".
func personName(question string, hits []search.Hit) string {
	words := compactWords(question)
	if len(words) == 0 {
		return ""
	}
	inQuestion := make(map[string]bool, len(words))
	for _, word := range words {
		inQuestion[word] = true
	}

	type candidate struct {
		name    string
		matched int
		hits    int
	}
	byName := make(map[string]*candidate, len(hits))
	for _, h := range hits {
		name := strings.TrimSpace(h.SenderName)
		if name == "" || strings.Contains(name, "@") {
			continue
		}
		tokens := compactWords(name)
		if len(tokens) < minPersonNameTokens {
			continue
		}
		role, matched := false, 0
		for _, token := range tokens {
			if roleMailboxWords[token] {
				role = true
				break
			}
			if inQuestion[token] && utf8.RuneCountInString(token) >= minPersonTokenRunes {
				matched++
			}
		}
		if role || matched == 0 {
			continue
		}
		key := personNameKey(name)
		if key == "" {
			continue
		}
		c := byName[key]
		if c == nil {
			c = &candidate{name: name}
			byName[key] = c
		}
		c.hits++
		if matched > c.matched {
			c.matched = matched
		}
	}

	var best *candidate
	ambiguous := false
	for _, c := range byName {
		switch {
		case best == nil || c.matched > best.matched ||
			(c.matched == best.matched && c.hits > best.hits):
			best, ambiguous = c, false
		case c.matched == best.matched && c.hits == best.hits:
			ambiguous = true
		}
	}
	if best == nil || ambiguous {
		return ""
	}
	return best.name
}

// personNameKey folds one display name for identity comparison, so
// "Nadia Tremblay" and "NADIA  TREMBLAY" are one person.
func personNameKey(name string) string {
	return mail.Fold(strings.Join(strings.Fields(name), " "))
}

// samePersonName compares two display names case-insensitively, ignoring
// spacing differences a mail client may introduce.
func samePersonName(a, b string) bool {
	return personNameKey(a) == personNameKey(b)
}

// personEvidence supplies the newest received messages from every address
// that writes under the person's display name. The question's own hits
// reveal the first addresses (the organization the question names); a search
// on the display name reveals the others, whose domains and signatures show
// the person's other organizations. The contact query is a newest-first
// received-message list; every row is checked against the exact sender
// address because the store's contact filter is a substring match. A store
// failure on one address is logged and skipped: the general hits remain the
// evidence. A failed name search is returned so the caller can log it and
// still answer from those hits.
func (s *Service) personEvidence(ctx context.Context, question string, hits []search.Hit, limit int) ([]contextMessage, error) {
	if limit <= 0 {
		return nil, nil
	}
	name := personName(question, hits)
	if name == "" {
		return nil, nil
	}
	found, err := s.search.Search(ctx, search.Query{Text: name,
		Filter: store.SearchFilter{OwnerOnly: true}, Limit: personSearchLimit})
	if err != nil {
		return nil, err
	}

	addresses := make([]string, 0, 4)
	seenAddress := map[string]bool{}
	addAddress := func(address string) {
		address = strings.TrimSpace(address)
		if address == "" {
			return
		}
		key := mail.NormalizeAddress(address)
		if seenAddress[key] {
			return
		}
		seenAddress[key] = true
		addresses = append(addresses, address)
	}
	for _, h := range hits {
		if samePersonName(h.SenderName, name) {
			addAddress(h.SenderAddress)
		}
	}
	for _, h := range found.Hits {
		if samePersonName(h.SenderName, name) {
			addAddress(h.SenderAddress)
		}
	}
	if len(addresses) == 0 {
		return nil, nil
	}

	// byMessage keys each message once and keeps every address that
	// produced its row, so the exact sender check below cannot drop a real
	// message when the LIKE contact filter matched it for several addresses.
	byMessage := make(map[int64]map[string]bool, len(addresses)*personRecentLimit)
	for _, address := range addresses {
		plan := mailquery.Plan{
			Kind: mailquery.KindMessages, Action: mailquery.ActionList,
			Direction: mailquery.DirectionReceived, Contact: address,
			TimeRange: mailquery.TimeAll, Limit: personRecentLimit, Order: mailquery.OrderDesc,
		}
		plan = plan.Normalize()
		if err := plan.Validate(); err != nil {
			s.log.Warn("qa person query invalid", "reason", errorClass(err))
			continue
		}
		result, err := s.store.ExecuteMailQuery(ctx, plan, s.zone())
		if err != nil {
			s.log.Warn("qa person query unavailable", "reason", errorClass(err))
			continue
		}
		for _, row := range result.Rows {
			if row.MessageID <= 0 {
				continue
			}
			if byMessage[row.MessageID] == nil {
				byMessage[row.MessageID] = map[string]bool{}
			}
			byMessage[row.MessageID][mail.NormalizeAddress(address)] = true
		}
	}
	if len(byMessage) == 0 {
		return nil, nil
	}
	ids := make([]int64, 0, len(byMessage))
	for id := range byMessage {
		ids = append(ids, id)
	}
	// A failed detail read only costs the To/Cc lines, learned labels and
	// read/importance facts; the message facts remain usable evidence, and
	// nothing is invented in their place (owner: contextMessages). The sender
	// check drops a co-participant's mail; the display name is this leg's own
	// decoration.
	msgs, err := s.contextMessages(ctx, ids, "person", "qa person context unavailable",
		func(id int64, sum store.Summary) bool { return personSender(byMessage[id], sum.SenderAddress) },
		func(msg *contextMessage) { msg.Person = name })
	if err != nil {
		return nil, err
	}
	sort.Slice(msgs, func(i, j int) bool {
		if msgs[i].ReceivedAt != msgs[j].ReceivedAt {
			return msgs[i].ReceivedAt > msgs[j].ReceivedAt
		}
		return msgs[i].ID > msgs[j].ID
	})
	if len(msgs) > limit {
		msgs = msgs[:limit]
	}
	return msgs, nil
}

// personSender reports whether one message's sender is one of the addresses
// that produced its row. The store's contact filter is a LIKE match, so the
// row alone does not prove the address; this exact check does.
func personSender(addresses map[string]bool, sender string) bool {
	return addresses[mail.NormalizeAddress(sender)]
}
