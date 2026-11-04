package qa

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mailquery"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/world"
)

// Entity resolution turns the name, address or domain a question mentions
// into the exact identities the mailbox knows. It is deterministic and local:
// one bounded person-candidate query plus the cached organization book, no
// provider call. A first name that several people share stays ambiguous: the
// owner picks one candidate, and the follow-up question carries that
// candidate's exact address or domain so clicking it resolves exactly.

const (
	entityPerson  = "person"
	entityCompany = "company"

	maxEntityCandidates  = 6
	maxEntityDomains     = 4
	entityCandidateLimit = 120
)

// resolvedEntity is one person or organization the question names, in the
// owner's own vocabulary: display name, exact normalized addresses and a
// declared company or the organization's domains. The company key is kept for
// the profile read, never shown.
type resolvedEntity struct {
	Kind      string
	Label     string
	Addresses []string
	Company   string
	Domains   []string
	// Contacts are a company's shared mailboxes on its own domain
	// (info@, rh@), the addresses a contact question can answer with when no
	// single person is the match.
	Contacts []string
	Messages int
	LastAt   string
	Evidence int
	Key      string
	Org      world.CompanyRow
	// Partial marks a near-miss: the question named a multi-token person and
	// this candidate matches only some of those tokens. A partial match is
	// never a "which person?" choice; it is offered as a clearly labelled
	// "did you mean?" alternative.
	Partial bool
}

// entityMentions is the deterministic vocabulary of one question: explicit
// addresses, explicit domains and the substantial name tokens left after the
// person/company question words are removed. A single stopword never selects
// a person.
type entityMentions struct {
	emails  []string
	domains []string
	terms   []string
}

var (
	entityEmailPattern = regexp.MustCompile(`[A-Za-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@` +
		`[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+`)
	entityDomainPattern = regexp.MustCompile(`(?i)\b[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?` +
		`(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+\b`)
)

// entityStopwords are request and summary words that cannot name a person.
// The summary verbs are taken from their one definition (summary.go); these
// add the greetings and request words that are not profile dimensions.
var entityStopwords = wordSet(strings.Join(withWords(summaryVerbList,
	"bonjour", "salut", "merci", "stp", "svp", "please", "hi", "hello", "hey",
	"question", "demande", "demander", "dis", "dire", "mailsense",
), " "))

// entityMention extracts the three mention forms. Email spans are removed
// before domain extraction, so a person's address does not also become a
// company domain.
func entityMention(question string) entityMentions {
	return entityMentionExcept(question, nil)
}

// entityMentionExcept is entityMention with terms excluded before the bounded
// term list fills, so a cue word cannot crowd out a later person term. ignore
// is nil for the ordinary resolver.
func entityMentionExcept(question string, ignore map[string]bool) entityMentions {
	folded := mail.Fold(question)
	var out entityMentions
	for _, address := range entityEmailPattern.FindAllString(folded, maxEntityCandidates) {
		address = mail.NormalizeAddress(address)
		if address != "" {
			out.emails = appendUniqueString(out.emails, address)
		}
	}
	withoutEmails := entityEmailPattern.ReplaceAllString(folded, " ")
	for _, domain := range entityDomainPattern.FindAllString(withoutEmails, maxEntityDomains) {
		domain = mail.NormalizeAddress(domain)
		if domain != "" {
			out.domains = appendUniqueString(out.domains, domain)
		}
	}
	withoutDomains := entityDomainPattern.ReplaceAllString(withoutEmails, " ")
	for _, word := range compactWords(withoutDomains) {
		if utf8.RuneCountInString(word) < 3 || entityStopwords[word] || companyProfileWords[word] ||
			companyQuestionStopwords[word] ||
			entityCompanyWords[word] || entityPersonWords[word] ||
			allDigits(word) || (ignore != nil && ignore[word]) {
			continue
		}
		out.terms = appendUniqueString(out.terms, word)
		if len(out.terms) == store.MaxPersonCandidateTerms {
			break
		}
	}
	return out
}

// aggregateCountEntityMentions narrows the person terms of a plain count: a
// count cue is not a name. Without this, "Amount of deadlines" resolves a
// sender named "Paramount ..." because personEvidence uses substring matching,
// and "Count of deadlines" can resolve "Accounting"; the plain count would
// then be deferred to freeform for the wrong reason. A category noun in the
// person-subject position is added back as a term, so a person actually named
// Deadline or Urgent still scopes the freeform follow-up; the same noun in
// the object position stays out of the person query.
func aggregateCountEntityMentions(question string) entityMentions {
	mentions := entityMentionExcept(question, aggregateCountCueSet)
	for _, subject := range aggregateCountPersonSubjects(mail.Fold(strings.TrimSpace(question))) {
		if !aggregateCountCategoryNouns[subject] {
			continue
		}
		if len(mentions.terms) >= store.MaxPersonCandidateTerms {
			break
		}
		mentions.terms = appendUniqueString(mentions.terms, subject)
	}
	return mentions
}

// resolveQuestionEntities returns every distinct identity the question names,
// best match first. Zero candidates leave the existing planner path; one
// candidate is an exact scope; several are a clarification. A failed optional
// read is logged and treated as no candidate, never as a reason to fail Ask or
// to guess an identity.
func (s *Service) resolveQuestionEntities(ctx context.Context, question string) []resolvedEntity {
	entities, _ := s.resolveQuestionEntitiesChecked(ctx, question, false)
	return entities
}

// resolveQuestionEntitiesChecked is the one entity resolver. strict makes a
// failed company-book or person-candidate read an error instead of a partial
// result. The provider-less count scope uses strict so a person-store failure
// cannot turn a person-scoped question into a mailbox-wide count; the freeform
// path keeps the tolerant behavior above.
func (s *Service) resolveQuestionEntitiesChecked(ctx context.Context, question string, strict bool) ([]resolvedEntity, error) {
	return s.resolveEntityMentionsChecked(ctx, entityMention(question), question, strict)
}

// resolveEntityMentionsChecked resolves pre-extracted mentions. It exists so
// the provider-less count scope can resolve the same question with count-cue
// terms removed without duplicating the company/person reads.
func (s *Service) resolveEntityMentionsChecked(ctx context.Context, mentions entityMentions, question string, strict bool) ([]resolvedEntity, error) {
	if len(mentions.emails) == 0 && len(mentions.domains) == 0 && len(mentions.terms) == 0 {
		return nil, nil
	}

	var companies []resolvedEntity
	if profiles := s.companyProfiles(); profiles != nil {
		res, err := profiles.List(ctx, world.ListQuery{Limit: companyListLimit})
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			s.log.Warn("qa company book unavailable for entity resolution", "reason", errorClass(err))
			if strict {
				return nil, err
			}
		} else {
			companies = companyEntityCandidates(res, question)
			companies = filterCompanyByDomains(companies, mentions.domains)
		}
	}

	var persons []resolvedEntity
	var contactPool []resolvedEntity
	if len(mentions.terms) > 0 || len(mentions.emails) > 0 {
		queryTerms := append(append([]string{}, mentions.emails...), mentions.terms...)
		rows, err := s.store.PersonCandidates(ctx, queryTerms, entityCandidateLimit)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			s.log.Warn("qa person candidates unavailable", "reason", errorClass(err))
			if strict {
				return nil, err
			}
		} else {
			persons = personEntityCandidates(rows, mentions)
			persons = filterPersonsByEmails(persons, mentions.emails)
			contactPool = contactCandidates(rows)
		}
	}
	companies = withCompanyContacts(companies, contactPool, question)
	return selectEntityCandidates(persons, companies, mentions, question), nil
}

// contactCandidates keeps every address the candidate read returned, without
// the person-evidence filter: a company contact answer needs info@ and rh@
// even though neither is a person the question names.
func contactCandidates(rows []store.PersonCandidate) []resolvedEntity {
	seen := map[string]bool{}
	out := make([]resolvedEntity, 0, len(rows))
	for _, row := range rows {
		address := mail.NormalizeAddress(row.Address)
		if address == "" || seen[address] {
			continue
		}
		seen[address] = true
		out = append(out, resolvedEntity{Kind: entityPerson, Label: oneLine(row.Name),
			Addresses: []string{address}, Messages: row.Messages})
	}
	return out
}

// companyEntityCandidates maps the organization book's matches to resolver
// candidates. Only display labels, domains and activity leave this function;
// a company key is never a label.
func companyEntityCandidates(book world.ListResult, question string) []resolvedEntity {
	matches := matchCompanies(question, book.Companies)
	out := make([]resolvedEntity, 0, len(matches))
	for _, match := range matches {
		label := oneLine(match.Org.Label)
		domains := cleanDomains(match.Org.Domains)
		if label == "" && len(domains) > 0 {
			label = domains[0]
		}
		if label == "" {
			continue
		}
		out = append(out, resolvedEntity{
			Kind: entityCompany, Label: label, Domains: domains,
			Messages: match.Org.Messages, LastAt: match.Org.LastAt,
			Evidence: match.Evidence, Key: match.Org.Key, Org: match.Org,
		})
	}
	return out
}

// filterCompanyByDomains makes an explicit domain in the question select its
// organization even when the original question also names a shared label. A
// clicked clarify option carries the domain for exactly this purpose.
func filterCompanyByDomains(candidates []resolvedEntity, domains []string) []resolvedEntity {
	if len(candidates) < 2 || len(domains) == 0 {
		return candidates
	}
	matched := make([]resolvedEntity, 0, len(candidates))
	for _, candidate := range candidates {
		for _, candidateDomain := range candidate.Domains {
			for _, domain := range domains {
				if strings.EqualFold(candidateDomain, domain) {
					matched = append(matched, candidate)
					goto next
				}
			}
		}
	next:
	}
	if len(matched) > 0 {
		return matched
	}
	return candidates
}

// personEntityCandidates merges the per-address store rows into people.
// Addresses merge only through identity evidence: one address-book entry, or
// the same display name on one registrable domain. A different name or the
// same name on unrelated domains stays a separate candidate. The best name is
// a declared address-book name, else the most used one.
func personEntityCandidates(rows []store.PersonCandidate, mentions entityMentions) []resolvedEntity {
	type addressAgg struct {
		address    string
		name       string
		company    string
		messages   int
		lastAt     string
		declared   bool
		nameCount  int
		contactKey string
	}
	byAddress := map[string]*addressAgg{}
	for _, row := range rows {
		address := mail.NormalizeAddress(row.Address)
		name := oneLine(row.Name)
		if strings.Contains(name, "@") {
			name = ""
		}
		if address == "" {
			continue
		}
		agg := byAddress[address]
		if agg == nil {
			agg = &addressAgg{address: address}
			byAddress[address] = agg
		}
		agg.messages += row.Messages
		if row.LastAt > agg.lastAt {
			agg.lastAt = row.LastAt
		}
		if agg.company == "" {
			agg.company = oneLine(row.Company)
		}
		if agg.contactKey == "" {
			agg.contactKey = row.ContactKey
		}
		agg.declared = agg.declared || row.Declared
		if betterPersonName(name, row.NameCount, row.Declared, agg.name, agg.nameCount, agg.declared) {
			agg.name, agg.nameCount = name, row.NameCount
		}
	}

	addresses := make([]string, 0, len(byAddress))
	for address := range byAddress {
		addresses = append(addresses, address)
	}
	sort.Strings(addresses)
	parent := make([]int, len(addresses))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[rb] = ra
		}
	}
	// Two addresses are one person only on identity evidence: the same
	// address-book entry, or the same display name on one registrable domain.
	// An identical name on unrelated domains stays two explicit candidates.
	firstByContact := map[string]int{}
	firstByNameDomain := map[string]int{}
	for i, address := range addresses {
		row := byAddress[address]
		if row.contactKey != "" {
			if first, ok := firstByContact[row.contactKey]; ok {
				union(first, i)
			} else {
				firstByContact[row.contactKey] = i
			}
		}
		if name := mail.Fold(row.name); name != "" {
			if domain := mail.RegistrableDomain(address); domain != "" {
				key := name + "|" + domain
				if first, ok := firstByNameDomain[key]; ok {
					union(first, i)
				} else {
					firstByNameDomain[key] = i
				}
			}
		}
	}

	type personAgg struct {
		name      string
		addresses []string
		company   string
		messages  int
		lastAt    string
		declared  bool
		nameCount int
	}
	byPerson := map[int]*personAgg{}
	for i, address := range addresses {
		row := byAddress[address]
		root := find(i)
		person := byPerson[root]
		if person == nil {
			person = &personAgg{}
			byPerson[root] = person
		}
		person.addresses = append(person.addresses, address)
		person.messages += row.messages
		if row.lastAt > person.lastAt {
			person.lastAt = row.lastAt
		}
		if person.company == "" {
			person.company = row.company
		}
		person.declared = person.declared || row.declared
		if betterPersonName(row.name, row.nameCount, row.declared,
			person.name, person.nameCount, person.declared) {
			person.name, person.nameCount = row.name, row.nameCount
		}
	}

	out := make([]resolvedEntity, 0, len(byPerson))
	for _, person := range byPerson {
		sort.Slice(person.addresses, func(i, j int) bool { return person.addresses[i] < person.addresses[j] })
		evidence := personEvidence(person.name, person.addresses, mentions)
		if evidence == 0 {
			continue
		}
		label := person.name
		if label == "" && len(person.addresses) > 0 {
			label = person.addresses[0]
		}
		out = append(out, resolvedEntity{Kind: entityPerson, Label: label, Addresses: person.addresses,
			Company: person.company, Messages: person.messages, LastAt: person.lastAt, Evidence: evidence})
	}
	sortEntities(out)
	return out
}

func betterPersonName(name string, nameCount int, declared bool, current string, currentCount int, currentDeclared bool) bool {
	if name == "" {
		return false
	}
	switch {
	case current == "":
		return true
	case declared != currentDeclared:
		return declared
	case nameCount != currentCount:
		return nameCount > currentCount
	default:
		return utf8.RuneCountInString(name) > utf8.RuneCountInString(current)
	}
}

// maxPartialPersonChoices bounds the "did you mean?" list. A near-miss list
// is a hint, not a directory: four names is enough to recognise the one the
// owner meant.
const maxPartialPersonChoices = 4

// nameShape is the address book's own name structure: which tokens behave
// like given names, which like surnames. It is derived from the candidates,
// never from a hardcoded list, so a mailbox with new names gets the same
// treatment. A token that only ever appears first is a given name; one that
// only ever appears last is a surname; a token that appears in both
// positions (Richard, a first name and a surname) is ambiguous and counts
// as either.
type nameShape struct {
	given   map[string]int // token → times it is the first token of a name
	surname map[string]int // token → times it is the last token of a name
}

func newNameShape(persons []resolvedEntity) nameShape {
	shape := nameShape{given: map[string]int{}, surname: map[string]int{}}
	for _, person := range persons {
		label := oneLine(person.Label)
		if label == "" || strings.Contains(label, "@") {
			continue
		}
		tokens := compactWords(label)
		if len(tokens) < 2 {
			continue
		}
		shape.given[tokens[0]]++
		shape.surname[tokens[len(tokens)-1]]++
	}
	return shape
}

// fullName reports whether the query terms look like a complete person name:
// at least two terms, one that behaves like a given name and one that behaves
// like a surname. A single token, or a set of tokens that only ever appear in
// one position, is not a full name and keeps the ordinary evidence ranking.
func (s nameShape) fullName(terms []string) bool {
	if len(terms) < 2 {
		return false
	}
	given, surname := false, false
	for _, term := range terms {
		if s.given[term] > 0 {
			given = true
		}
		if s.surname[term] > 0 {
			surname = true
		}
	}
	return given && surname
}

// personNameTerms keeps the question terms that name a person: a term that
// appears in at least one candidate's display name. "richard" and "harvey"
// are name terms; a company word that only appears in an organization's
// label is not.
func personNameTerms(terms []string, persons []resolvedEntity) []string {
	out := make([]string, 0, len(terms))
	for _, term := range terms {
		for _, person := range persons {
			if nameHasToken(person.Label, term) {
				out = append(out, term)
				break
			}
		}
	}
	return out
}

// nameHasToken reports whether a display name repeats a compact term.
func nameHasToken(name, term string) bool {
	for _, token := range compactWords(name) {
		if token == term {
			return true
		}
	}
	return false
}

// personsMatchingAll keeps the people whose identity repeats every term.
func personsMatchingAll(persons []resolvedEntity, terms []string) []resolvedEntity {
	out := make([]resolvedEntity, 0, len(persons))
	for _, person := range persons {
		matched := true
		for _, term := range terms {
			if !entityHasToken(person, term) {
				matched = false
				break
			}
		}
		if matched {
			out = append(out, person)
		}
	}
	return out
}

// topPartialPersons keeps the near-misses that match the most name terms,
// then the rarest term (a surname shared by two people is a better hint than
// a first name shared by four), then the most messages, bounded to
// maxPartialPersonChoices.
func topPartialPersons(persons []resolvedEntity, terms []string, limit int) []resolvedEntity {
	hits := make(map[string]int, len(terms))
	for _, term := range terms {
		for _, person := range persons {
			if entityHasToken(person, term) {
				hits[term]++
			}
		}
	}
	type scored struct {
		person  resolvedEntity
		matched int
		rarity  float64
	}
	ranked := make([]scored, 0, len(persons))
	for _, person := range persons {
		matched := 0
		rarity := 0.0
		for _, term := range terms {
			if !entityHasToken(person, term) {
				continue
			}
			matched++
			if hits[term] > 0 {
				rarity += 1 / float64(hits[term])
			}
		}
		if matched > 0 {
			ranked = append(ranked, scored{person: person, matched: matched, rarity: rarity})
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].matched != ranked[j].matched {
			return ranked[i].matched > ranked[j].matched
		}
		if ranked[i].rarity != ranked[j].rarity {
			return ranked[i].rarity > ranked[j].rarity
		}
		if ranked[i].person.Messages != ranked[j].person.Messages {
			return ranked[i].person.Messages > ranked[j].person.Messages
		}
		return mail.Fold(ranked[i].person.Label) < mail.Fold(ranked[j].person.Label)
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	out := make([]resolvedEntity, 0, len(ranked))
	for _, entry := range ranked {
		out = append(out, entry.person)
	}
	return out
}

// appendUniqueEntities appends the candidates whose identity is not already
// present (same kind, label and first address).
func appendUniqueEntities(dst []resolvedEntity, extra ...resolvedEntity) []resolvedEntity {
	seen := make(map[string]bool, len(dst))
	for _, entity := range dst {
		seen[entityIdentity(entity)] = true
	}
	for _, entity := range extra {
		key := entityIdentity(entity)
		if seen[key] {
			continue
		}
		seen[key] = true
		dst = append(dst, entity)
	}
	return dst
}

func entityIdentity(entity resolvedEntity) string {
	address := ""
	if len(entity.Addresses) > 0 {
		address = entity.Addresses[0]
	}
	return entity.Kind + "|" + entity.Label + "|" + address
}

// personEvidence counts the distinct substantial question tokens the person's
// name or addresses repeat; an exact address mention is the strongest signal.
func personEvidence(name string, addresses []string, mentions entityMentions) int {
	// A term is a whole name or address token, never an inner substring:
	// "liste" must not score the sender "listening@lenovo.com" and
	// "fiscalistes" must not score it through the domain. compactWords folds
	// case and accents and splits compound names and addresses, so a term is
	// compared with exactly one token.
	tokens := compactWords(name + " " + strings.Join(addresses, " "))
	present := make(map[string]bool, len(tokens))
	for _, token := range tokens {
		present[token] = true
	}
	evidence := 0
	for _, term := range mentions.terms {
		if present[term] {
			evidence++
		}
	}
	for _, email := range mentions.emails {
		for _, address := range addresses {
			if strings.EqualFold(email, address) {
				evidence += 2
				break
			}
		}
	}
	return evidence
}

// filterPersonsByEmails makes an explicit address select its person even when
// the original question also repeats a shared first name.
func filterPersonsByEmails(candidates []resolvedEntity, emails []string) []resolvedEntity {
	if len(candidates) < 2 || len(emails) == 0 {
		return candidates
	}
	matched := make([]resolvedEntity, 0, len(candidates))
	for _, candidate := range candidates {
		for _, address := range candidate.Addresses {
			for _, email := range emails {
				if strings.EqualFold(address, email) {
					matched = append(matched, candidate)
					goto next
				}
			}
		}
	next:
	}
	if len(matched) > 0 {
		return matched
	}
	return candidates
}

// selectEntityCandidates applies the owner's wording first (company words
// choose a company, person words choose a person), then an explicit email or
// domain, then the strongest match. Distinct identities are never merged.
// An address is the strongest person signal: a company matched only because
// its domain appears inside that address must not turn an addressed person
// into a company/person choice. A bare domain is the strongest company
// signal, even when a person from that domain also matches.
func selectEntityCandidates(persons, companies []resolvedEntity, mentions entityMentions, question string) []resolvedEntity {
	words := compactWords(question)
	companyWord := containsAny(words, entityCompanyWords)
	personWord := containsAny(words, entityPersonWords)

	// A word that names several organizations (or several people) is a
	// category, not an identity. Drop candidates whose only match is such a
	// generic word, and give a person a composite bonus when the question
	// names their company as well. The same organization reached through two
	// labels (Services Exemple and Info - Services E.X.) is one identity.
	generic := genericEntityTerms(mentions.terms, persons, companies)
	persons = withCompositeCompany(persons, companies, generic)
	companies = dropGenericCandidates(companies, mentions.terms, generic)
	companies = mergeCompaniesByDomain(companies)
	// A multi-token person name is one identity: "richard harvey" is not
	// "Olivier Robert" or "David Harvey". When the question gives two or more
	// name terms, a person must match all of them, or be at the organization
	// the question names. If nobody does, the near-misses stay as clearly
	// labelled partial matches instead of a "which person?" choice between
	// unrelated people.
	if nameTerms := personNameTerms(mentions.terms, persons); newNameShape(persons).fullName(nameTerms) {
		full := personsMatchingAll(persons, nameTerms)
		full = appendUniqueEntities(full, personsAtNamedCompany(persons, companies, question)...)
		if len(full) > 0 {
			persons = full
		} else {
			persons = topPartialPersons(persons, nameTerms, maxPartialPersonChoices)
			for i := range persons {
				persons[i].Partial = true
			}
		}
	}

	var selected []resolvedEntity
	switch {
	case companyWord && len(companies) > 0:
		selected = companies
	case len(mentions.emails) > 0 && len(persons) > 0:
		selected = persons
	case len(mentions.domains) > 0 && len(companies) > 0:
		selected = companies
	case personWord && len(persons) > 0:
		selected = persons
	default:
		// The question may name only the organization even though several
		// people work on its domain: "les 5 dernières demandes de clinique exemple"
		// asks about the organization's mail, not one of its mailboxes. A
		// confident company wins unless the question repeats a term that
		// belongs to a person candidate and not to the company.
		if len(companies) > 0 && companies[0].Evidence >= minCompanyConfident &&
			!personTermOutsideCompany(persons, companies, mentions.terms) {
			selected = companies
			break
		}
		// A person at the organization the question names is the obvious
		// answer: "le courriel de simon de services ex" is one person,
		// not a six-way choice between every Simon and every mailbox.
		if atCompany := personsAtNamedCompany(persons, companies, question); len(atCompany) > 0 {
			selected = atCompany
			break
		}
		switch {
		case len(persons) > 0 && len(companies) > 0:
			switch {
			case persons[0].Evidence > companies[0].Evidence:
				selected = persons
			case companies[0].Evidence > persons[0].Evidence:
				selected = companies
			default:
				selected = append(append([]resolvedEntity{}, persons...), companies...)
			}
		case len(persons) > 0:
			selected = persons
		default:
			selected = companies
		}
	}
	if len(selected) == 0 {
		return nil
	}
	// A "which person?" choice never offers a shared mailbox: info@, rh@
	// and support@ are not people. When the question names a real person
	// among them, the mailbox is not an alternative identity.
	if selected[0].Kind == entityPerson {
		if people := personLikeOnly(selected); len(people) > 0 {
			selected = people
		}
	}
	// A near-miss list is already ranked by name-token rarity, and every
	// near-miss matches one token, so the evidence margin does not apply.
	if selected[0].Partial {
		if len(selected) > maxEntityCandidates {
			selected = selected[:maxEntityCandidates]
		}
		return selected
	}
	sortEntities(selected)
	// A decisive winner answers; a near-tie stays a choice. The margin is
	// measured in evidence points, so an exact address or a person-at-company
	// domain always wins, while two people who share only a first name do not.
	if len(selected) > 1 && selected[0].Evidence >= selected[1].Evidence+entityDecisiveMargin {
		selected = selected[:1]
	} else {
		best := selected[0].Evidence
		out := make([]resolvedEntity, 0, len(selected))
		for _, candidate := range selected {
			if candidate.Evidence == best {
				out = append(out, candidate)
			}
		}
		selected = out
	}
	if len(selected) > maxEntityCandidates {
		selected = selected[:maxEntityCandidates]
	}
	return selected
}

// entityDecisiveMargin is how far ahead the best candidate must be before the
// resolver answers instead of asking. Two candidates that only share a first
// name stay tied; a person at the named company or an exact address is two or
// more points ahead.
const entityDecisiveMargin = 2

// personsAtNamedCompany returns the people who work on a domain of a
// confidently named organization. It is the composite rule behind "simon
// de services ex": the question names a person and a company, and the person
// at that company is the answer. Shared mailboxes (info@, rh@) are not people
// and never win this route.
func personsAtNamedCompany(persons, companies []resolvedEntity, question string) []resolvedEntity {
	if len(persons) == 0 || len(companies) == 0 {
		return nil
	}
	domains := map[string]bool{}
	for _, company := range companies {
		if company.Evidence < minCompanyConfident {
			continue
		}
		for _, domain := range company.Domains {
			if name := mail.RegistrableName(domain); name != "" {
				domains[name] = true
			}
		}
	}
	if len(domains) == 0 {
		return nil
	}
	out := make([]resolvedEntity, 0, 2)
	for _, person := range persons {
		if person.Evidence == 0 || !person.isPersonLike() {
			continue
		}
		for _, address := range person.Addresses {
			if domains[mail.RegistrableDomain(address)] {
				out = append(out, person)
				break
			}
		}
	}
	return out
}

// personTermOutsideCompany reports a question term that belongs to one of
// the person candidates but not to any named company's identity. When the
// question has no such term, it names the organization, not one of its
// mailboxes.
func personTermOutsideCompany(persons, companies []resolvedEntity, terms []string) bool {
	if len(persons) == 0 || len(companies) == 0 || len(terms) == 0 {
		return false
	}
	companyTokens := map[string]bool{}
	for _, company := range companies {
		org := company.Org
		if strings.TrimSpace(org.Key) == "" {
			// A directly built candidate still carries its visible identity;
			// the linker's token owner covers its label, domains and bases.
			org = world.CompanyRow{Label: company.Label, Domains: company.Domains}
		}
		for _, token := range companyIdentityTokens(org) {
			if token != "" {
				companyTokens[token] = true
			}
		}
	}
	for _, person := range persons {
		personTokens := map[string]bool{}
		for _, token := range compactWords(person.Label) {
			personTokens[token] = true
		}
		for _, address := range person.Addresses {
			local := localPart(address)
			for _, token := range compactWords(local) {
				personTokens[token] = true
			}
			if token := mail.CompactFold(local); token != "" {
				personTokens[token] = true
			}
		}
		for _, term := range terms {
			if personTokens[term] && !companyTokens[term] {
				return true
			}
		}
	}
	return false
}

// minCompanyConfident is the score a company match needs before a person on
// its domain is treated as the person the question means. It is above a
// generic-word-only match, which scores nothing.
const minCompanyConfident = 20

// mergeCompaniesByDomain folds the labels the book keeps for one domain into
// one identity. "Services Exemple" and "Info - Services E.X." are one mailbox
// organization; the best-scored label represents it.
func mergeCompaniesByDomain(companies []resolvedEntity) []resolvedEntity {
	if len(companies) < 2 {
		return companies
	}
	out := make([]resolvedEntity, 0, len(companies))
	index := map[string]int{}
	for _, company := range companies {
		domain := ""
		for _, candidate := range company.Domains {
			if name := mail.RegistrableName(candidate); name != "" {
				domain = name
				break
			}
		}
		if domain == "" {
			out = append(out, company)
			continue
		}
		if at, ok := index[domain]; ok {
			if betterCompany(company, out[at]) {
				out[at] = company
			}
			continue
		}
		index[domain] = len(out)
		out = append(out, company)
	}
	return out
}

func betterCompany(candidate, current resolvedEntity) bool {
	if candidate.Evidence != current.Evidence {
		return candidate.Evidence > current.Evidence
	}
	if candidate.Messages != current.Messages {
		return candidate.Messages > current.Messages
	}
	return candidate.Label < current.Label
}

// personLikeOnly keeps the candidates that name a person (empty: none does).
// It is the one place a shared mailbox is separated from a person.
func personLikeOnly(candidates []resolvedEntity) []resolvedEntity {
	out := make([]resolvedEntity, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.isPersonLike() {
			out = append(out, candidate)
		}
	}
	return out
}

// isPersonLike distinguishes a person from a shared mailbox. A display name of
// two or more words that contains no mailbox or company-suffix word is a
// person; otherwise the local part must look personal and not generic. This
// is what keeps info@, rh@ and support@ out of the "which person?" choice.
func (e resolvedEntity) isPersonLike() bool {
	words := compactWords(e.Label)
	if len(words) >= 2 && !containsAny(words, roleMailboxWords) && !containsAny(words, companySuffixWords) {
		return true
	}
	for _, address := range e.Addresses {
		local := localPart(address)
		if local == "" || roleMailboxWords[local] || allDigits(local) {
			continue
		}
		if strings.ContainsAny(local, "._-") || utf8.RuneCountInString(local) >= 3 {
			return true
		}
	}
	return false
}

var companySuffixWords = wordSet("inc ltd ltee corp corporation company companies compagnie " +
	"compagnies entreprise entreprises societe societes services service gestion solutions " +
	"technologies technology systems system group groupe")

// genericEntityTerms marks a term that names several candidates. It is
// computed from the current candidate pool, never from a phrase list: a word
// shared by three or more organizations (services, info, gestion) is a
// category, while a distinctive word (exemple, simon) is not.
func genericEntityTerms(terms []string, persons, companies []resolvedEntity) map[string]bool {
	out := map[string]bool{}
	for _, term := range terms {
		orgHits, personHits := 0, 0
		for _, company := range companies {
			if entityHasToken(company, term) {
				orgHits++
			}
		}
		for _, person := range persons {
			if entityHasToken(person, term) {
				personHits++
			}
		}
		if orgHits >= 3 || personHits >= 3 {
			out[term] = true
		}
	}
	return out
}

// entityHasToken reports whether one candidate's visible identity repeats a
// compact term. Addresses, declared company and display label are all part of
// the identity; a raw key never is.
func entityHasToken(candidate resolvedEntity, term string) bool {
	text := candidate.Label + " " + candidate.Company + " " +
		strings.Join(candidate.Domains, " ") + " " + strings.Join(candidate.Addresses, " ")
	for _, token := range compactWords(text) {
		if token == term {
			return true
		}
	}
	return false
}

// dropGenericCandidates removes a candidate whose only query evidence is a
// generic term. A distinctive term, an explicit email or a domain keeps it.
// A candidate the linker already scored as a confident organization keeps its
// identity too: its evidence came from its own label, acronym or domain, and a
// word that is generic across the person pool (several mailboxes on the same
// domain) does not make the organization a category.
func dropGenericCandidates(candidates []resolvedEntity, terms []string, generic map[string]bool) []resolvedEntity {
	if len(generic) == 0 {
		return candidates
	}
	out := candidates[:0]
	for _, candidate := range candidates {
		if candidate.Evidence >= minCompanyConfident {
			out = append(out, candidate)
			continue
		}
		matched := false
		specific := false
		for _, term := range terms {
			if !entityHasToken(candidate, term) {
				continue
			}
			matched = true
			if !generic[term] {
				specific = true
				break
			}
		}
		if !matched || specific {
			out = append(out, candidate)
		}
	}
	return out
}

// withCompositeCompany gives a person a bonus when the question also names
// their organization: an address on one of the organization's domains is the
// strongest signal, then a declared company label sharing a distinctive word.
// This is what makes "simon de services ex" resolve to the Simon at
// Services E.X. instead of asking which of three Simons is meant.
func withCompositeCompany(persons, companies []resolvedEntity, generic map[string]bool) []resolvedEntity {
	if len(persons) == 0 || len(companies) == 0 {
		return persons
	}
	out := make([]resolvedEntity, len(persons))
	copy(out, persons)
	for i := range out {
		person := &out[i]
		bonus := 0
		matchedCompany := ""
		for _, company := range companies {
			for _, domain := range company.Domains {
				for _, address := range person.Addresses {
					if strings.EqualFold(mail.RegistrableDomain(address), mail.RegistrableName(domain)) {
						bonus = 2
						matchedCompany = company.Label
						break
					}
				}
				if bonus == 2 {
					break
				}
			}
			if bonus != 0 {
				break
			}
		}
		if bonus == 0 {
			companyTokens := map[string]bool{}
			for _, token := range compactWords(person.Company) {
				if !generic[token] {
					companyTokens[token] = true
				}
			}
			for _, company := range companies {
				for _, token := range compactWords(company.Label + " " + strings.Join(company.Domains, " ")) {
					if companyTokens[token] {
						bonus = 1
						matchedCompany = company.Label
						break
					}
				}
				if bonus != 0 {
					break
				}
			}
		}
		if bonus == 2 && person.Company == "" {
			person.Company = matchedCompany
		}
		person.Evidence += bonus
	}
	return out
}

var (
	entityCompanyWords = wordSet("client clients entreprise entreprises compagnie compagnies " +
		"societe societes company companies org organization organisation organisations business " +
		"fournisseur fournisseurs")
	entityPersonWords = wordSet("avec personne personnes contact contacts mr mrs ms m mme " +
		"monsieur madame collegue collegues ami amie")
)

// pluralEntityWords names a scope that is several people or organizations.
// Such a question must never be reduced to one candidate: the owner is asking
// for the whole set, not for one member of it. The set is vocabulary, not a
// per-question phrase list; a new wording only needs its role noun here.
var pluralEntityWords = wordSet("clients clientes contacts personnes gens destinataires " +
	"recipients interlocuteurs fournisseurs fournisseurs entreprises compagnies organisations " +
	"societes companies people suppliers vendors organizations businesses")

// listingWords is the shape of a list request. It is paired with a plural role
// noun, so "what did the clients say" stays a content question while "which
// clients mentioned X" is an aggregate list.
var listingWords = wordSet("liste list quels quelles lesquels lesquelles which who qui " +
	"tous toutes all every each chaque plusieurs several combien how many")

// entityContentCues are verbs and question words that ask what was said, not
// who belongs to a set. They keep a content question out of the topic lane.
var entityContentCues = wordSet("dit disent say says said raconte racontent resume resumer " +
	"summarize summarise explique expliquent explain pourquoi why comment how quoi what")

// pluralEntityQuestion reports a question whose subject is several people or
// organizations. It is deliberately narrower than listingEntityQuestion: it
// only names the plural roles, so a singular "messages de Nadia" keeps its
// exact person scope.
func pluralEntityQuestion(question string) bool {
	return containsAny(compactWords(question), pluralEntityWords)
}

// listingEntityQuestion is the one shape that may execute a people/company
// topic ranking even though the question starts with "who" or "which": a
// listing cue, a plural role noun, and no "what did they say" cue.
func listingEntityQuestion(question string) bool {
	words := compactWords(question)
	if !containsAny(words, pluralEntityWords) || !containsAny(words, listingWords) {
		return false
	}
	return !containsAny(words, entityContentCues)
}

// sortEntities orders best match first, then activity, then a stable label and
// address so the clarification buttons never reorder between requests.
func sortEntities(out []resolvedEntity) {
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Evidence != b.Evidence {
			return a.Evidence > b.Evidence
		}
		if a.Messages != b.Messages {
			return a.Messages > b.Messages
		}
		if a.LastAt != b.LastAt {
			return a.LastAt > b.LastAt
		}
		if la, lb := mail.Fold(a.Label), mail.Fold(b.Label); la != lb {
			return la < lb
		}
		return strings.Join(a.Addresses, ",") < strings.Join(b.Addresses, ",")
	})
}

// entityClarification is the one-click question for several candidates.
// Options are complete follow-up questions; the candidate's exact address or
// domain is in the option text, so the existing UI needs no opaque id.
func entityClarification(question, language string, candidates []resolvedEntity) *mailquery.Clarification {
	if len(candidates) < 2 {
		return nil
	}
	options := make([]string, 0, len(candidates))
	structured := make([]mailquery.ClarifyCandidate, 0, len(candidates))
	seen := map[string]bool{}
	for _, candidate := range candidates {
		option := entityFollowUp(question, entityCandidateLabel(candidate))
		if option == "" || seen[option] {
			continue
		}
		seen[option] = true
		options = append(options, option)
		choice := mailquery.ClarifyCandidate{Label: candidate.Label, Messages: candidate.Messages,
			LastAt: candidate.LastAt, Question: option, Kind: "person"}
		if candidate.Kind == entityCompany {
			choice.Kind = "company"
			if domains := cleanDomains(candidate.Domains); len(domains) > 0 {
				choice.Domain = domains[0]
			}
		} else if len(candidate.Addresses) > 0 {
			choice.Address = candidate.Addresses[0]
		}
		structured = append(structured, choice)
	}
	if len(options) < 2 {
		return nil
	}
	prompt := entityClarifyPrompt(language, candidates)
	return &mailquery.Clarification{Question: prompt, Options: options, Candidates: structured}
}

// entityClarifyPrompt names the kind the owner is choosing. A mixed set asks
// about the contact without assuming a person or a company.
func entityClarifyPrompt(language string, candidates []resolvedEntity) string {
	// A near-miss list is not a "which person?" choice: the question named a
	// full name nobody here has, so the prompt says so.
	partial := len(candidates) > 0
	for _, candidate := range candidates {
		if !candidate.Partial {
			partial = false
			break
		}
	}
	if partial {
		if isFrench(language) {
			return "Personne ne porte ce nom complet. Voulez-vous dire…"
		}
		return "No one has that full name. Did you mean…"
	}
	company, person := false, false
	for _, candidate := range candidates {
		switch candidate.Kind {
		case entityCompany:
			company = true
		case entityPerson:
			person = true
		}
	}
	switch {
	case company && !person:
		if isFrench(language) {
			return "De quel client parlez-vous ?"
		}
		return "Which client do you mean?"
	case person && !company:
		if isFrench(language) {
			return "De quelle personne parlez-vous ?"
		}
		return "Which person do you mean?"
	default:
		if isFrench(language) {
			return "De quel contact parlez-vous ?"
		}
		return "Which contact do you mean?"
	}
}

// entityCandidateLabel is the stable, owner-visible label of one candidate.
// It always carries the identifier the follow-up must resolve: an address for
// a person, a domain for a company.
func entityCandidateLabel(candidate resolvedEntity) string {
	name := oneLine(candidate.Label)
	switch candidate.Kind {
	case entityCompany:
		domain := ""
		if len(candidate.Domains) > 0 {
			domain = oneLine(candidate.Domains[0])
		}
		switch {
		case name != "" && domain != "":
			return name + " (" + domain + ")"
		case domain != "":
			return domain
		default:
			return name
		}
	case entityPerson:
		if len(candidate.Addresses) == 0 {
			return name
		}
		addresses := candidate.Addresses
		label := name + " <" + strings.Join(addresses, ", ") + ">"
		if name == "" {
			label = "<" + strings.Join(addresses, ", ") + ">"
		}
		if len(label) > 180 {
			if name == "" {
				return "<" + addresses[0] + ">"
			}
			return name + " <" + addresses[0] + ">"
		}
		return label
	default:
		return name
	}
}

// entityFollowUp appends the candidate label to the original question. The
// trailing question mark is normalized, so the option stays one complete
// question the owner can click.
func entityFollowUp(question, label string) string {
	base := strings.TrimSpace(question)
	base = strings.TrimRight(base, " \t\r\n.?!")
	label = strings.TrimSpace(label)
	if base == "" || label == "" {
		return ""
	}
	suffix := " — " + label + " ?"
	option := base + suffix
	if len(option) <= 300 {
		return option
	}
	// The store's clarification bound is 300 bytes; keep the identifier and
	// shorten only the repeated question.
	const ellipsis = "…"
	keep := 300 - len(suffix) - len(ellipsis)
	if keep < 1 {
		return ""
	}
	cut := base
	for len(cut) > keep {
		cut = cut[:len(cut)-1]
	}
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	cut = strings.TrimRight(cut, " \t\r\n")
	if cut == "" {
		return ""
	}
	return cut + ellipsis + suffix
}

// questionNamesEntity reports whether the question itself mentions this
// person or organization, which lets a plan without a contact filter still be
// scoped to the resolved identity.
func questionNamesEntity(question string, entity resolvedEntity) bool {
	if entity.Kind == entityCompany {
		words := compactWords(question)
		for _, alias := range append([]string{entity.Label}, entity.Domains...) {
			target := mail.CompactFold(alias)
			if utf8.RuneCountInString(target) >= minCompanyAliasRunes && containsCompactPhrase(words, target) {
				return true
			}
		}
		return false
	}
	foldedQuestion := mail.Fold(question)
	for _, address := range entity.Addresses {
		if strings.Contains(foldedQuestion, address) {
			return true
		}
	}
	questionWords := compactWords(question)
	for _, word := range compactWords(entity.Label) {
		if utf8.RuneCountInString(word) < minPersonTokenRunes || roleMailboxWords[word] {
			continue
		}
		for _, questionWord := range questionWords {
			if questionWord == word {
				return true
			}
		}
	}
	return false
}

// planContactMatchesEntity reports whether a plan's free-text contact names
// the resolved person. A hit is rewritten to exact addresses; a miss keeps the
// plan the planner chose.
func planContactMatchesEntity(contact string, entity resolvedEntity) bool {
	contact = mail.Fold(oneLine(contact))
	if contact == "" || entity.Kind != entityPerson {
		return false
	}
	if contact == mail.Fold(entity.Label) {
		return true
	}
	for _, address := range entity.Addresses {
		if contact == mail.Fold(address) || strings.Contains(contact, mail.Fold(address)) {
			return true
		}
	}
	for _, word := range compactWords(entity.Label) {
		if utf8.RuneCountInString(word) >= minPersonTokenRunes && strings.Contains(contact, word) {
			return true
		}
	}
	return false
}

// applyResolvedEntity rewrites a planner plan that names the resolved person
// (or whose question is about that person) to exact addresses. The substring
// contact is removed with the rewrite, so a first name can no longer match
// hundreds of unrelated messages.
func applyResolvedEntity(plan mailquery.Plan, entity *resolvedEntity, question string) mailquery.Plan {
	if entity == nil || entity.Kind != entityPerson || len(entity.Addresses) == 0 {
		return plan
	}
	named := questionNamesEntity(question, *entity)
	contact := planContactMatchesEntity(plan.Contact, *entity)
	switch plan.Kind {
	case mailquery.KindMessages:
		if !named && !contact {
			return plan
		}
	case mailquery.KindContacts, mailquery.KindCompanies:
		if !contact {
			return plan
		}
	default:
		return plan
	}
	plan.ContactAddresses = append([]string{}, entity.Addresses...)
	plan.Contact = ""
	return plan.Normalize()
}

// writeResolvedEntity renders the resolved identity for the planner. It shows
// names, addresses and domains only; a company key never leaves the service.
func writeResolvedEntity(b *strings.Builder, entity *resolvedEntity) {
	if entity == nil {
		return
	}
	name := oneLine(entity.Label)
	b.WriteString("\nResolved entity (exact; never broaden it):\n")
	switch entity.Kind {
	case entityCompany:
		b.WriteString("Company: ")
		b.WriteString(name)
		if domains := cleanDomains(entity.Domains); len(domains) > 0 {
			b.WriteString(" (")
			b.WriteString(strings.Join(domains, ", "))
			b.WriteString(")")
		}
		b.WriteString("\n")
	case entityPerson:
		b.WriteString("Person: ")
		b.WriteString(name)
		if len(entity.Addresses) > 0 {
			b.WriteString(" <")
			b.WriteString(strings.Join(entity.Addresses, ", "))
			b.WriteString(">")
		}
		if company := oneLine(entity.Company); company != "" {
			b.WriteString("; company: ")
			b.WriteString(company)
		}
		b.WriteString("\n")
	}
}

func appendUniqueString(in []string, value string) []string {
	for _, existing := range in {
		if existing == value {
			return in
		}
	}
	return append(in, value)
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
