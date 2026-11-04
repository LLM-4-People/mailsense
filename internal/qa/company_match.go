package qa

import (
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/world"
)

// Company linking is a scored match, not a phrase table. A question names an
// organization through its label, a domain, a domain prefix, an acronym
// ("E.X." for "Services E.X."), an abbreviation ("sl" for "Exemple") or a small
// spelling variation. Every form is folded to the same token space, generic
// words shared by several organizations carry no identity, and the score
// records how much distinctive evidence the question gave. The single-result
// linker still refuses two distinct organizations; the resolver can then ask
// or use a person-at-company composite.

const (
	// minCompanyAliasRunes is the shortest compact alias that may select an
	// organization. Two-letter labels occur in ordinary prose ("la", "ab"),
	// so a shorter alias would turn entity linking into a keyword match.
	minCompanyAliasRunes = 3
	// minCompanyPrefixRunes is the shortest domain-base prefix that may
	// select an organization ("cirrus" for cirrus-media.test). The prefix must
	// also leave at least two characters of the base unmatched, and two
	// different matches stay ambiguous.
	minCompanyPrefixRunes = 5
	// minCompanyEvidence is the floor a scored match must reach. A generic
	// word alone never reaches it because generic tokens score nothing.
	minCompanyEvidence = 10
)

// companyMatch is one organization the question names, with the score of the
// distinctive evidence the question gave. Several distinct matches stay
// separate so the resolver can ask which one the owner means; the
// single-result linker returns a company only when exactly one matched.
type companyMatch struct {
	Org      world.CompanyRow
	Evidence int
}

// companyTokenStats is the book-wide token frequency. A token shared by three
// or more organizations is a category word ("services", "info"), not an
// identity; a token only one organization uses is distinctive.
type companyTokenStats struct {
	freq map[string]int
}

func newCompanyTokenStats(companies []world.CompanyRow) companyTokenStats {
	stats := companyTokenStats{freq: map[string]int{}}
	for _, org := range companies {
		seen := map[string]bool{}
		for _, token := range companyIdentityTokens(org) {
			if token == "" || seen[token] {
				continue
			}
			seen[token] = true
			stats.freq[token]++
		}
	}
	return stats
}

// generic reports a token that cannot identify one organization on its own.
// The closed list covers the words every mailbox book repeats; the frequency
// rule catches a book-specific category word the list does not know. A short
// acronym ("sl") is not generic just because it is short: the caller skips
// single letters, and the closed list holds the two-letter function words.
func (s companyTokenStats) generic(token string) bool {
	if token == "" {
		return true
	}
	if companyGenericTokens[token] {
		return true
	}
	return s.freq[token] >= 3
}

// companyIdentityTokens is every word and acronym an organization's label and
// domains expose, used only to measure how widely a token is shared.
func companyIdentityTokens(org world.CompanyRow) []string {
	out := make([]string, 0, 8)
	out = append(out, companyLabelTokens(compactWords(org.Label))...)
	for _, domain := range org.Domains {
		out = append(out, compactWords(domain)...)
	}
	for _, base := range companyDomainBases(org) {
		out = append(out, mail.CompactFold(base))
	}
	return out
}

// companyLabelTokens is the label's words plus the acronyms its consecutive
// single-letter words form ("E.X." -> "sl") and the initials of all its words
// ("Services E.X." -> "ssl"). The acronym is how prose writes a company name.
func companyLabelTokens(words []string) []string {
	out := make([]string, 0, len(words)+2)
	out = append(out, words...)
	run := ""
	initials := ""
	for _, word := range words {
		if utf8.RuneCountInString(word) == 1 {
			run += word
		} else if run != "" {
			out = append(out, run)
			run = ""
		}
		if word != "" {
			initials += string([]rune(word)[0])
		}
	}
	if run != "" {
		out = append(out, run)
	}
	if utf8.RuneCountInString(initials) >= 2 {
		out = append(out, initials)
	}
	return out
}

// matchCompanies is the pure entity linker. It returns every distinct
// organization whose distinctive evidence reaches the floor, deduplicated by
// key and ordered by score, then activity.
func matchCompanies(question string, companies []world.CompanyRow) []companyMatch {
	words := compactWords(question)
	if len(words) == 0 {
		return nil
	}
	stats := newCompanyTokenStats(companies)
	byKey := make(map[string]companyMatch, 1)
	for _, org := range companies {
		if strings.TrimSpace(org.Key) == "" {
			continue
		}
		score, distinct := companyEvidence(words, org, stats)
		if distinct == 0 || score < minCompanyEvidence {
			continue
		}
		if previous, ok := byKey[org.Key]; !ok || score > previous.Evidence {
			byKey[org.Key] = companyMatch{Org: org, Evidence: score}
		}
	}
	matches := make([]companyMatch, 0, len(byKey))
	for _, match := range byKey {
		matches = append(matches, match)
	}
	sort.Slice(matches, func(i, j int) bool {
		a, b := matches[i], matches[j]
		if a.Evidence != b.Evidence {
			return a.Evidence > b.Evidence
		}
		if a.Org.Messages != b.Org.Messages {
			return a.Org.Messages > b.Org.Messages
		}
		if a.Org.LastAt != b.Org.LastAt {
			return a.Org.LastAt > b.Org.LastAt
		}
		if la, lb := mail.Fold(a.Org.Label), mail.Fold(b.Org.Label); la != lb {
			return la < lb
		}
		return a.Org.Key < b.Org.Key
	})
	return matches
}

// companyEvidence scores one organization against the question. It returns
// the score and the number of distinctive tokens that matched; a match with
// no distinctive token is not an identity.
func companyEvidence(words []string, org world.CompanyRow, stats companyTokenStats) (int, int) {
	labelWords := compactWords(org.Label)
	score, distinct := 0, 0

	// A contiguous label run is the strongest label evidence: "Services E.X."
	// matches the question's "services ex" as one compact phrase, and "Iso
	// Santé" matches "clinique-exemple". A run made only of generic words is not an
	// identity.
	for start := 0; start < len(labelWords); start++ {
		phrase := ""
		for end := start; end < len(labelWords); end++ {
			phrase += labelWords[end]
			if utf8.RuneCountInString(phrase) < minCompanyAliasRunes {
				continue
			}
			if !containsCompactPhrase(words, phrase) {
				continue
			}
			runDistinct := 0
			for _, token := range labelWords[start : end+1] {
				if utf8.RuneCountInString(token) < 2 || stats.generic(token) {
					continue
				}
				runDistinct++
			}
			if runDistinct == 0 {
				continue
			}
			if candidate := 20*runDistinct + 8*(end-start+1); candidate > score {
				score = candidate
			}
			if runDistinct > distinct {
				distinct = runDistinct
			}
		}
	}

	// Individual label words, acronyms and abbreviations. "sl" is the
	// acronym of "E.X." and an abbreviation of "Exemple"; both are identity
	// evidence, the exact form stronger than the abbreviation.
	for _, token := range companyLabelTokens(labelWords) {
		if utf8.RuneCountInString(token) < 2 || stats.generic(token) {
			continue
		}
		switch {
		case containsToken(words, token):
			score += 12
			distinct++
		case abbreviationInQuestion(words, token):
			score += 7
			distinct++
		case fuzzyInQuestion(words, token):
			score += 4
			distinct++
		}
	}

	// The domain is the organization's other name. The registrable domain's
	// first label is the prose form ("cirrus" for cirrus-media.test); a
	// subdomain such as services.delta-systemes.test must not turn the generic
	// "services" into Delta Systèmes's identity.
	for _, domain := range org.Domains {
		if containsCompactPhrase(words, mail.CompactFold(domain)) {
			score += 50
			distinct++
		}
	}
	for _, base := range companyDomainBases(org) {
		target := mail.CompactFold(base)
		if target == "" || stats.generic(target) {
			continue
		}
		if containsToken(words, target) || containsCompactPhrase(words, target) {
			score += 40
			distinct++
			continue
		}
		for i, word := range words {
			wordRunes := utf8.RuneCountInString(word)
			targetRunes := utf8.RuneCountInString(target)
			if wordRunes < minCompanyPrefixRunes || targetRunes < wordRunes+2 {
				continue
			}
			if strings.HasPrefix(target, word) && companyPrefixCue(words, i) {
				score += 16
				distinct++
				break
			}
		}
	}
	return score, distinct
}

// matchCompany is the single-result form used by structured questions: it
// links only when exactly one distinct organization matched. Two or more
// matches stay unresolved for the planner or the entity resolver.
func matchCompany(question string, companies []world.CompanyRow) (world.CompanyRow, bool) {
	matches := matchCompanies(question, companies)
	if len(matches) != 1 {
		return world.CompanyRow{}, false
	}
	return matches[0].Org, true
}

// containsToken reports one whole compact token.
func containsToken(words []string, target string) bool {
	for _, word := range words {
		if word == target {
			return true
		}
	}
	return false
}

// abbreviationInQuestion reports a short question token that abbreviates a
// longer label token: "sl" for "exemple" (first and last letter kept, the rest
// omitted in order). The short form is at most three runes and the long form
// at most five, so an ordinary word is never treated as an abbreviation.
func abbreviationInQuestion(words []string, target string) bool {
	targetRunes := []rune(target)
	if len(targetRunes) < 3 || len(targetRunes) > 5 {
		return false
	}
	for _, word := range words {
		runes := []rune(word)
		if len(runes) < 2 || len(runes) > 3 {
			continue
		}
		if runes[0] != targetRunes[0] || runes[len(runes)-1] != targetRunes[len(targetRunes)-1] {
			continue
		}
		at := 0
		for _, r := range targetRunes {
			if at < len(runes) && r == runes[at] {
				at++
			}
		}
		if at == len(runes) {
			return true
		}
	}
	return false
}

// fuzzyInQuestion reports a one-edit spelling variation of a label token. The
// lengths must be within one rune, so a derived word ("atelieresque") never
// matches its stem.
func fuzzyInQuestion(words []string, target string) bool {
	targetRunes := []rune(target)
	if len(targetRunes) < 5 {
		return false
	}
	for _, word := range words {
		runes := []rune(word)
		if len(runes) < 5 || absInt(len(runes)-len(targetRunes)) > 1 {
			continue
		}
		if editDistanceAtMostOne(runes, targetRunes) {
			return true
		}
	}
	return false
}

func editDistanceAtMostOne(a, b []rune) bool {
	if len(a) > len(b) {
		a, b = b, a
	}
	if len(b)-len(a) > 1 {
		return false
	}
	i, j, edits := 0, 0, 0
	for i < len(a) && j < len(b) {
		if a[i] == b[j] {
			i++
			j++
			continue
		}
		edits++
		if edits > 1 {
			return false
		}
		if len(a) == len(b) {
			i++
			j++
		} else {
			j++
		}
	}
	if i < len(a) || j < len(b) {
		edits++
	}
	return edits <= 1
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// companyDomainPrefix reports whether one question word is a distinctive
// prefix of the organization's domain base. It is deliberately narrow: the
// base must be at least two runes longer than the word, and the word must be
// at least minCompanyPrefixRunes, so "cirrus" selects cirrus-media.test while
// "proj" and "cirrusical" do not.
func companyDomainPrefix(words []string, org world.CompanyRow) bool {
	for _, base := range companyDomainBases(org) {
		target := mail.CompactFold(base)
		targetRunes := utf8.RuneCountInString(target)
		if targetRunes < minCompanyPrefixRunes+2 {
			continue
		}
		for i, word := range words {
			wordRunes := utf8.RuneCountInString(word)
			if wordRunes < minCompanyPrefixRunes || targetRunes < wordRunes+2 {
				continue
			}
			if strings.HasPrefix(target, word) && companyPrefixCue(words, i) {
				return true
			}
		}
	}
	return false
}

// companyPrefixCue distinguishes a company name from a common noun that
// happens to start the domain. A fuzzy prefix is only entity evidence when
// the sentence scopes it as one: after a relation word ("de", "with",
// "from"), unless a quantifier turns the following noun into the thing being
// counted ("combien de logement"), or as the subject of a speech/contact
// verb ("cirrus a répondu", "what did cirrus say").
func companyPrefixCue(words []string, at int) bool {
	if at > 0 && companyRelationWords[words[at-1]] {
		if at < 2 || !companyQuantifierWords[words[at-2]] {
			return true
		}
	}
	for i := at + 1; i < len(words) && i <= at+3; i++ {
		if companyActionWords[words[i]] {
			return true
		}
	}
	// "Qui est cirrus ?" names the organization as the answer, not as a
	// common noun; a who/what question with the prefix last is entity
	// evidence even without a verb.
	if at == len(words)-1 && containsAny(words[:at], whoWords) {
		return true
	}
	return false
}

var companyRelationWords = wordSet("de du des d avec chez pour concernant sur from with about of")

var companyQuantifierWords = wordSet("combien nombre quantite total beaucoup peu plus moins tant assez trop aucun chaque many much some any all")

var companyActionWords = wordSet("repondu repond repondre say says said tell tells told write writes wrote " +
	"mention mentions mentioned contact contacte contactee appele appelee call called email emailed " +
	"dit dis dire ecrit ecrite ecrits ecrites envoie envoye envoyee demander demande approuve paye signe")

// companyGenericTokens are the words a mailbox book repeats across unrelated
// organizations. They never identify one organization on their own.
var companyGenericTokens = wordSet("services service info infos information informations " +
	"support contact contacts sales admin administration team equipe group groupe " +
	"solutions technologies technology systems system inc ltd ltee corp corporation " +
	"company companies compagnie compagnies entreprise entreprises societe societes " +
	"the and et de du des la le les of for pour to in on at by is an or")

// companyDomainBases is each domain's registrable first label ("cirrusmedia"
// for cirrus-media.test, "delta-systemes" for services.delta-systemes.test), the form people
// write in prose. A subdomain is not the organization's name.
func companyDomainBases(org world.CompanyRow) []string {
	out := make([]string, 0, len(org.Domains))
	for _, domain := range org.Domains {
		domain = strings.TrimSpace(domain)
		if domain == "" {
			continue
		}
		registrable := mail.RegistrableName(domain)
		if registrable == "" {
			registrable = domain
		}
		base := registrable
		if i := strings.IndexByte(base, '.'); i > 0 {
			base = base[:i]
		}
		if base != "" {
			out = append(out, base)
		}
	}
	return out
}

// companyAliases are the names a question may use: the display label, each
// full domain, and the domain's registrable first label. The domain base is
// what people write in prose ("clinique-exemple" for clinique-exemple.test).
func companyAliases(org world.CompanyRow) []string {
	aliases := make([]string, 0, 1+2*len(org.Domains))
	aliases = append(aliases, org.Label)
	for _, domain := range org.Domains {
		domain = strings.TrimSpace(domain)
		if domain == "" {
			continue
		}
		aliases = append(aliases, domain)
	}
	aliases = append(aliases, companyDomainBases(org)...)
	return aliases
}

// companyAliasEvidence counts how many of one alias's compact words the
// question repeats. A whole compact form (for example a dotted domain) counts
// as one when it matched as a contiguous phrase.
func companyAliasEvidence(words []string, alias string) int {
	inQuestion := make(map[string]bool, len(words))
	for _, word := range words {
		inQuestion[word] = true
	}
	evidence := 0
	for _, word := range compactWords(alias) {
		if inQuestion[word] {
			evidence++
		}
	}
	if evidence == 0 {
		return 1
	}
	return evidence
}

// maxCompanyContacts bounds the shared mailboxes a company contact answer
// lists. The owner needs the right desk, not the whole directory.
const maxCompanyContacts = 3

// withCompanyContacts attaches the shared mailboxes on each company's own
// domain, ranked by the question's intent: an employee question prefers rh@,
// an invoice question facturation@, a support question support@. A company
// contact answer is then useful even when no single person is the match.
func withCompanyContacts(companies, persons []resolvedEntity, question string) []resolvedEntity {
	if len(companies) == 0 || len(persons) == 0 {
		return companies
	}
	wanted := contactIntentLocals(question)
	for i := range companies {
		domains := map[string]bool{}
		for _, domain := range companies[i].Domains {
			if name := mail.RegistrableName(domain); name != "" {
				domains[name] = true
			}
		}
		if len(domains) == 0 {
			continue
		}
		type contact struct {
			address  string
			score    int
			messages int
		}
		seen := map[string]bool{}
		list := make([]contact, 0, 4)
		for _, person := range persons {
			for _, address := range person.Addresses {
				if !domains[mail.RegistrableDomain(address)] || seen[address] {
					continue
				}
				local := localPart(address)
				if !roleMailboxWords[local] {
					continue
				}
				seen[address] = true
				score := 0
				for _, want := range wanted {
					if strings.Contains(local, want) {
						score = 10
						break
					}
				}
				list = append(list, contact{address: address, score: score, messages: person.Messages})
			}
		}
		sortContacts := func() {
			sort.Slice(list, func(a, b int) bool {
				if list[a].score != list[b].score {
					return list[a].score > list[b].score
				}
				if list[a].messages != list[b].messages {
					return list[a].messages > list[b].messages
				}
				return list[a].address < list[b].address
			})
		}
		sortContacts()
		// When the question's desk does not exist, the general desk is the
		// right fallback, not whichever mailbox happens to be busiest.
		matchedIntent := false
		for _, entry := range list {
			if entry.score > 0 {
				matchedIntent = true
				break
			}
		}
		if !matchedIntent {
			for at := range list {
				if defaultContactLocals[localPart(list[at].address)] {
					list[at].score = 5
				}
			}
			sortContacts()
		}
		for at := 0; at < len(list) && at < maxCompanyContacts; at++ {
			companies[i].Contacts = append(companies[i].Contacts, list[at].address)
		}
	}
	return companies
}

// contactIntentLocals maps the question's subject to the mailbox local parts
// that answer it. The default is the general desk.
func contactIntentLocals(question string) []string {
	words := compactWords(question)
	switch {
	case containsAny(words, employeeWords):
		return []string{"rh", "hr", "emploi", "recrutement", "personnel", "paie", "payroll"}
	case containsAny(words, invoiceWords):
		return []string{"facturation", "facture", "billing", "compta", "accounting", "paiement"}
	case containsAny(words, supportWords):
		return []string{"support", "aide", "help", "technique", "service"}
	case containsAny(words, salesWords):
		return []string{"vente", "sales", "commercial", "devis", "soumission"}
	default:
		return defaultContactDesks
	}
}

var (
	employeeWords = wordSet("employe employes employee employees embauche embauches hiring " +
		"recrutement recruitment rh hr personnel paie payroll")
	invoiceWords = wordSet("facture factures invoice invoices paiement payment compta " +
		"comptabilite accounting billing")
	supportWords = wordSet("support aide help technique technical incident panne")
	salesWords   = wordSet("vente ventes sales commercial devis quote soumission")
)

// defaultContactDesks is the general desk, in probe order: a contact answer
// tries these local parts when the question's own desk does not exist.
// defaultContactLocals is the same desk as a set, so the probe order and the
// membership test cannot drift.
var defaultContactDesks = []string{"info", "contact", "bonjour", "hello", "accueil", "reception"}

var defaultContactLocals = wordSet(strings.Join(defaultContactDesks, " "))

// localPart is the lowercased part before @.
func localPart(address string) string {
	local := mail.NormalizeAddress(address)
	if i := strings.IndexByte(local, '@'); i >= 0 {
		local = local[:i]
	}
	return local
}
