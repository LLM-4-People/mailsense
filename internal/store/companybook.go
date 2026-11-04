package store

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/mail"
)

// Configuration keys this file and the company profile read. They are named
// once here so the book and the profile can never disagree on their source.
const ()

// A domain naming at least this many unrelated declared companies (over at
// least this many declared addresses) is shared in structure, not one
// organization. The counts are deliberately conservative: a corporate
// domain with a few legal-name spellings stays one company, while a
// free-mail domain used by many unrelated companies cannot dominate.
const (
	companySharedMinCompanies = 4
	companySharedMinAddresses = 5
)

// ErrUnknownCompany reports a company key that no visible address book or
// resolvable non-shared domain produces.
var ErrUnknownCompany = errors.New("store: unknown company")

// CompanyPerson is one address of an organization. A person read from the
// visible address books is Declared; a counterpart seen only in mail is not.
// The mail totals are observed values: they are zero for a declared-only
// person, and the address-book fields are empty when no entry carries them.
type CompanyPerson struct {
	Address    string `json:"address"`
	Name       string `json:"name,omitempty"`
	Company    string `json:"company,omitempty"`
	Department string `json:"department,omitempty"`
	JobTitle   string `json:"job_title,omitempty"`
	Phone      string `json:"phone,omitempty"`
	Declared   bool   `json:"declared"`

	Messages int    `json:"messages"`
	Sent     int    `json:"sent"`
	Received int    `json:"received"`
	LastAt   string `json:"last_at,omitempty"`
}

// CompanyOrg is one organization resolved from the visible address books and
// the owner identities. A declared company merges every address that declares
// it across domains, and its Domains lists the non-shared registrable domains
// those addresses belong to; a domain organization is the fallback of a
// domain nobody declared. Owner domains and companies are resolved too and
// are reported internal by IsInternal.
type CompanyOrg struct {
	Key       string          `json:"key"`
	Label     string          `json:"label"`
	Declared  bool            `json:"declared"`
	Domains   []string        `json:"domains"`
	Addresses []string        `json:"addresses"`
	People    []CompanyPerson `json:"people"`
}

// clone returns an organization the caller may keep or mutate without
// changing the book.
func (o CompanyOrg) clone() CompanyOrg {
	o.Domains = append([]string{}, o.Domains...)
	o.Addresses = append([]string{}, o.Addresses...)
	o.People = append([]CompanyPerson{}, o.People...)
	return o
}

// CompanyCoverage is how much of the visible address book the book could
// turn into organizations. Contacts and ContactsWithCompany count distinct
// contact addresses; Domains counts their non-shared registrable domains.
// DeclaredOrgs counts every declared organization, including the owner's
// own companies (OwnerCompanies makes that subset explicit).
type CompanyCoverage struct {
	Contacts            int `json:"contacts"`
	ContactsWithCompany int `json:"contacts_with_company"`
	Domains             int `json:"domains"`
	DeclaredOrgs        int `json:"declared_orgs"`
	OwnerDomains        int `json:"owner_domains"`
	OwnerCompanies      int `json:"owner_companies"`
}

// CompanyBook resolves addresses to organizations from what the visible
// address books declare. It is the one company-resolution owner of the
// store: the company ranking and the deep company profile both read it.
//
// A shared domain is never used as a domain-level company: the declared
// company of one free-mail contact must not label every unrelated person on
// that service. An exact declared company wins over the dominant company of
// the address's non-shared registrable domain, which wins over the domain
// itself. A shared domain without a declaration has no organization.
type CompanyBook struct {
	exact          map[string]string // address_norm -> most frequent declared company
	domain         map[string]string // non-shared registrable domain -> dominant declared company
	domains        mail.Domains
	shared         map[string]bool // structurally shared domains with no domain org
	ownerDomains   map[string]bool
	ownerCompanies map[string]bool
	orgs           map[string]CompanyOrg
	coverage       CompanyCoverage

	// The book reads contacts and the owner identities, not mail. These
	// fingerprints let a mail write reuse the installed book while the
	// address book is unchanged; a contact or owner write changes them.
	contactFingerprint string
	ownerFingerprint   string
}

// companyKey is one address's resolution before the full organization is
// attached: the key the caller groups by, the declared spelling (if any)
// and the address's non-shared registrable domain ("" for a shared one).
type companyKey struct {
	key      string
	label    string
	declared bool
	domain   string
}

// companyResolution is a resolution with the organization it names.
type companyResolution struct {
	org      CompanyOrg
	spelling string // declared spelling of this address, "" for a domain org
	domain   string // non-shared registrable domain of this address, "" if shared
}

// CompanyBook reads the visible address books and the owners. Only picked
// stores' contacts take part (the same s.pickedKeys() scope as every other
// contact read). Owner domains and the companies declared on them are
// internal, even when an external address declares the same company.
func (s *Store) CompanyBook(ctx context.Context) (*CompanyBook, error) {
	return s.book.get(ctx, s.MailGeneration(), s.cfg.Snapshot(), false,
		func(buildCtx context.Context, snap *config.Snapshot, previous *CompanyBook, _ bool) (*CompanyBook, error) {
			// The test seam runs inside the detached build, the same place the
			// counterparts seam runs, so it observes the build the waiters join.
			if hook := s.bookBuildHook; hook != nil {
				hook()
			}
			return s.buildCompanyBookFrom(buildCtx, snap, previous)
		})
}

// companyContactRowsQuery is the one address-book row stream of the book:
// the contact identity (for a stable fingerprint) plus the fields the
// resolution reads. The build appends its fold order; the fingerprint pass
// does not need one.
const companyContactRowsQuery = `SELECT c.store_key, c.entry_id, ce.ordinal, ce.address_norm,
		c.display_name, c.company, c.department, c.job_title, c.phone
		FROM contact_emails ce
		JOIN contacts c ON c.id = ce.contact_id
		WHERE c.store_key IN ` + stringSetSQL

// buildCompanyBookFrom reuses the installed book when a mail generation
// moved but the address book and owner identities did not. Contacts and
// owners are the book's only database inputs; a mail write cannot change
// them. A changed fingerprint falls through to the complete build, which
// recomputes both fingerprints.
func (s *Store) buildCompanyBookFrom(ctx context.Context, snap *config.Snapshot,
	previous *CompanyBook) (*CompanyBook, error) {
	if previous != nil {
		contact, err := s.companyBookContactFingerprint(ctx, snap)
		if err != nil {
			return nil, err
		}
		owners, err := s.owners(ctx, snap)
		if err != nil {
			return nil, err
		}
		if hex.EncodeToString(contact[:]) == previous.contactFingerprint &&
			companyBookOwnerFingerprint(owners) == previous.ownerFingerprint {
			return previous, nil
		}
		// The fingerprint pass already streamed every contact row, so the
		// build reuses its digest instead of hashing the same rows twice.
		return s.buildCompanyBook(ctx, snap, &contact)
	}
	return s.buildCompanyBook(ctx, snap, nil)
}

// companyBookContactFingerprint hashes the address-book rows into one
// order-independent value. Each row carries its store/entry/ordinal identity,
// so deleting one contact and adding another with the same fields still
// changes the digest unless the rows are byte-identical.
func (s *Store) companyBookContactFingerprint(ctx context.Context,
	snap *config.Snapshot) ([sha256.Size]byte, error) {
	rows, err := s.db.QueryContext(ctx, companyContactRowsQuery, jsonList(s.pickedKeysOf(snap)))
	if err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("store: company contact fingerprint: %w", err)
	}
	defer rows.Close()
	var sum [sha256.Size]byte
	for rows.Next() {
		var storeKey, entryID, address, name, company, department, jobTitle, phone string
		var ordinal int
		if err := rows.Scan(&storeKey, &entryID, &ordinal, &address, &name, &company,
			&department, &jobTitle, &phone); err != nil {
			return [sha256.Size]byte{}, fmt.Errorf("store: company contact fingerprint row: %w", err)
		}
		address = mail.NormalizeAddress(address)
		company = strings.TrimSpace(company)
		if address == "" {
			continue
		}
		row := companyContactRowHash(storeKey, entryID, ordinal, address, name, company, department, jobTitle, phone)
		for i := range sum {
			sum[i] ^= row[i]
		}
	}
	if err := rows.Err(); err != nil {
		return [sha256.Size]byte{}, fmt.Errorf("store: company contact fingerprint rows: %w", err)
	}
	return sum, nil
}

// companyBookOwnerFingerprint names the owner identity set the book derives
// its internal domains and companies from. scope.Identity owns the canonical
// address form; this only orders the stores deterministically.
func companyBookOwnerFingerprint(owners map[string]Owner) string {
	keys := make([]string, 0, len(owners))
	for key := range owners {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, key := range keys {
		writeCompanyFingerprintField(h, key)
		writeCompanyFingerprintField(h, owners[key].Identity.Digest())
	}
	return hex.EncodeToString(h.Sum(nil))
}

// companyContactRowHash is one contact row's contribution to the address-book
// fingerprint. Length prefixes keep field boundaries unambiguous.
func companyContactRowHash(storeKey, entryID string, ordinal int, address, name, company, department,
	jobTitle, phone string) [sha256.Size]byte {
	h := sha256.New()
	writeCompanyFingerprintField(h, storeKey)
	writeCompanyFingerprintField(h, entryID)
	writeCompanyFingerprintField(h, strconv.Itoa(ordinal))
	writeCompanyFingerprintField(h, address)
	writeCompanyFingerprintField(h, name)
	writeCompanyFingerprintField(h, company)
	writeCompanyFingerprintField(h, department)
	writeCompanyFingerprintField(h, jobTitle)
	writeCompanyFingerprintField(h, phone)
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out
}

func writeCompanyFingerprintField(h hash.Hash, value string) {
	var length [8]byte
	binary.LittleEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = h.Write(length[:])
	_, _ = h.Write([]byte(value))
}

// buildCompanyBook reads one complete book without touching the cache. A
// non-nil contactFingerprint is the digest of the rows this call reads: the
// caller already computed it, so the fold only accumulates the book.
func (s *Store) buildCompanyBook(ctx context.Context, snap *config.Snapshot,
	contactFingerprint *[sha256.Size]byte) (*CompanyBook, error) {
	domains := mail.NewDomains(snap.Strings(mail.SettingSharedDomains))
	owners, err := s.owners(ctx, snap)
	if err != nil {
		return nil, err
	}
	ownerDomains := map[string]bool{}
	for _, owner := range owners {
		for _, address := range owner.Identity.Addresses {
			if domain := domains.Of(address); domain != "" {
				ownerDomains[domain] = true
			}
		}
	}

	rows, err := s.db.QueryContext(ctx, companyContactRowsQuery+`
		ORDER BY ce.address_norm, (trim(c.company) = ''), c.company, c.display_name, c.store_key, c.entry_id, ce.ordinal`,
		jsonList(s.pickedKeysOf(snap)))
	if err != nil {
		return nil, fmt.Errorf("store: company address book: %w", err)
	}
	defer rows.Close()

	var builtFingerprint [sha256.Size]byte
	if contactFingerprint != nil {
		builtFingerprint = *contactFingerprint
	}
	exactCounts := map[string]map[string]int{}
	domainCounts := map[string]map[string]int{}
	ownerCompanies := map[string]bool{}
	people := map[string]CompanyPerson{}
	contactSet := map[string]bool{}
	withCompany := map[string]bool{}
	domainSet := map[string]bool{}
	for rows.Next() {
		var storeKey, entryID, address, name, company, department, jobTitle, phone string
		var ordinal int
		if err := rows.Scan(&storeKey, &entryID, &ordinal, &address, &name, &company,
			&department, &jobTitle, &phone); err != nil {
			return nil, fmt.Errorf("store: company address row: %w", err)
		}
		address = mail.NormalizeAddress(address)
		company = strings.TrimSpace(company)
		if address == "" {
			continue
		}
		if contactFingerprint == nil {
			rowHash := companyContactRowHash(storeKey, entryID, ordinal, address, name, company,
				department, jobTitle, phone)
			for i := range builtFingerprint {
				builtFingerprint[i] ^= rowHash[i]
			}
		}
		contactSet[address] = true
		person, found := people[address]
		if !found {
			person = CompanyPerson{Address: address, Declared: true}
		}
		if person.Name == "" {
			person.Name = strings.TrimSpace(name)
		}
		if person.Company == "" {
			person.Company = company
		}
		if person.Department == "" {
			person.Department = strings.TrimSpace(department)
		}
		if person.JobTitle == "" {
			person.JobTitle = strings.TrimSpace(jobTitle)
		}
		if person.Phone == "" {
			person.Phone = strings.TrimSpace(phone)
		}
		people[address] = person

		domain := domains.Of(address)
		if domain != "" {
			domainSet[domain] = true
		}
		if company == "" {
			continue
		}
		withCompany[address] = true
		addCompanySpelling(exactCounts, address, company)
		if domain != "" {
			addCompanySpelling(domainCounts, domain, company)
			if ownerDomains[domain] {
				ownerCompanies["c:"+mail.Fold(company)] = true
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: company address rows: %w", err)
	}

	exact := make(map[string]string, len(exactCounts))
	for address, counts := range exactCounts {
		exact[address] = dominantCompanySpelling(counts)
	}
	byDomain := make(map[string]string, len(domainCounts))
	for domain, counts := range domainCounts {
		byDomain[domain] = dominantCompanySpelling(counts)
	}

	// A domain that names several unrelated declared companies is shared by
	// those companies even when the structural learner has not listed it yet
	// (a free-mail domain whose senders sometimes share a thread is the hard
	// case). Treating its dominant company as the whole domain would merge
	// unrelated people into one invented organization. Exact declarations
	// still resolve; only the domain fallback is removed.
	declaredCompanies := map[string]map[string]bool{}
	declaredAddresses := map[string]int{}
	for address, company := range exact {
		if company == "" {
			continue
		}
		// Use the raw registrable domain: this guard is the evidence that
		// survives after the domain has been learned as shared (Domains.Of
		// then returns ""), so the learner can keep it.
		domain := mail.RegistrableDomain(address)
		if domain == "" {
			continue
		}
		if declaredCompanies[domain] == nil {
			declaredCompanies[domain] = map[string]bool{}
		}
		declaredCompanies[domain][mail.Fold(company)] = true
		declaredAddresses[domain]++
	}
	shared := map[string]bool{}
	for domain, companies := range declaredCompanies {
		if len(companies) >= companySharedMinCompanies && declaredAddresses[domain] >= companySharedMinAddresses {
			shared[domain] = true
			delete(byDomain, domain)
		}
	}

	orgs := map[string]*CompanyOrg{}
	labels := map[string]map[string]int{}
	ensure := func(key string, declared bool) *CompanyOrg {
		org := orgs[key]
		if org == nil {
			org = &CompanyOrg{Key: key, Declared: declared,
				Domains: []string{}, Addresses: []string{}, People: []CompanyPerson{}}
			orgs[key] = org
		}
		return org
	}

	// A declared organization's label is the dominant spelling among its
	// addresses, the same weighting the company ranking reports.
	for _, company := range exact {
		key := "c:" + mail.Fold(company)
		ensure(key, true)
		if labels[key] == nil {
			labels[key] = map[string]int{}
		}
		labels[key][company]++
	}
	// A domain's dominant company is backed by the addresses that declare
	// it; only an organization no address spelling reaches falls back to
	// the domain declaration for its label.
	for domain, company := range byDomain {
		key := "c:" + mail.Fold(company)
		org := ensure(key, true)
		if len(labels[key]) == 0 {
			labels[key] = map[string]int{company: 1}
		}
		org.Domains = append(org.Domains, domain)
	}

	for address, person := range people {
		resolved, ok := companyKeyOf(exact, byDomain, domains, shared, address)
		if !ok {
			continue
		}
		org := ensure(resolved.key, resolved.declared)
		if company := exact[address]; company != "" {
			person.Company = company
		}
		org.Addresses = append(org.Addresses, address)
		if resolved.domain != "" {
			org.Domains = append(org.Domains, resolved.domain)
		}
		org.People = append(org.People, person)
	}

	coverage := CompanyCoverage{
		Contacts:            len(contactSet),
		ContactsWithCompany: len(withCompany),
		Domains:             len(domainSet),
		OwnerDomains:        len(ownerDomains),
		OwnerCompanies:      len(ownerCompanies),
	}
	outOrgs := make(map[string]CompanyOrg, len(orgs))
	for key, org := range orgs {
		if org.Declared {
			org.Label = dominantCompanySpelling(labels[key])
			if org.Label == "" {
				org.Label = strings.TrimPrefix(key, "c:")
			}
			coverage.DeclaredOrgs++
		} else {
			org.Label = strings.TrimPrefix(key, "d:")
		}
		org.Domains = sortedUniqueStrings(org.Domains)
		org.Addresses = sortedUniqueStrings(org.Addresses)
		sort.Slice(org.People, func(i, j int) bool { return org.People[i].Address < org.People[j].Address })
		outOrgs[key] = *org
	}

	book := &CompanyBook{
		exact:              exact,
		domain:             byDomain,
		domains:            domains,
		shared:             shared,
		ownerDomains:       ownerDomains,
		ownerCompanies:     ownerCompanies,
		orgs:               outOrgs,
		coverage:           coverage,
		contactFingerprint: hex.EncodeToString(builtFingerprint[:]),
		ownerFingerprint:   companyBookOwnerFingerprint(owners),
	}
	return book, nil
}

// companyKeyOf applies the one resolution rule without attaching the full
// organization: an exact declared company, then the dominant declared
// company of a non-shared registrable domain, then the domain itself.
func companyKeyOf(exact, byDomain map[string]string, domains mail.Domains, shared map[string]bool, address string) (companyKey, bool) {
	address = mail.NormalizeAddress(address)
	domain := domains.Of(address)
	if shared[domain] {
		domain = ""
	}
	if company := exact[address]; company != "" {
		return companyKey{key: "c:" + mail.Fold(company), label: company, declared: true, domain: domain}, true
	}
	if domain == "" {
		return companyKey{}, false
	}
	if company := byDomain[domain]; company != "" {
		return companyKey{key: "c:" + mail.Fold(company), label: company, declared: true, domain: domain}, true
	}
	return companyKey{key: "d:" + domain, label: domain, domain: domain}, true
}

// resolve is the private form the company ranking uses: it also exposes the
// pair's declared spelling and domain, which the ranking aggregates but the
// public organization does not carry.
func (b *CompanyBook) resolve(address string) (companyResolution, bool) {
	key, ok := companyKeyOf(b.exact, b.domain, b.domains, b.shared, address)
	if !ok {
		return companyResolution{}, false
	}
	org, found := b.orgs[key.key]
	if !found {
		org = CompanyOrg{Key: key.key, Label: key.label, Declared: key.declared,
			Domains: []string{}, Addresses: []string{}, People: []CompanyPerson{}}
		if key.domain != "" {
			org.Domains = []string{key.domain}
		}
	}
	spelling := ""
	if key.declared {
		spelling = key.label
	}
	return companyResolution{org: org, spelling: spelling, domain: key.domain}, true
}

// Resolve returns the organization of one address. Exact declared company
// wins; then a dominant declared company of a non-shared domain; then the
// domain itself. A shared domain without a declaration has no organization.
// Owner organizations are returned too: IsInternal tells the caller.
func (b *CompanyBook) Resolve(address string) (CompanyOrg, bool) {
	resolved, ok := b.resolve(address)
	if !ok {
		return CompanyOrg{}, false
	}
	return resolved.org.clone(), true
}

// ResolveExternal is Resolve restricted to addresses outside the owner:
// owner domains, owner companies and shared domains without a declaration
// have no external organization. The digest labels a conversation only with
// this answer, so an internal colleague can never appear as a customer.
func (b *CompanyBook) ResolveExternal(address string) (CompanyOrg, bool) {
	resolved, ok := b.resolve(address)
	if !ok || b.isInternalResolution(resolved) {
		return CompanyOrg{}, false
	}
	return resolved.org.clone(), true
}

// Org returns the organization with this key. A domain key resolves when the
// domain is non-shared and no declared company dominates it: exactly the
// domain organizations Resolve can produce, even when no contact records
// the domain. An unknown declared-company key, a shared domain and a
// dominated domain do not resolve.
func (b *CompanyBook) Org(key string) (CompanyOrg, bool) {
	key = strings.TrimSpace(key)
	if org, ok := b.orgs[key]; ok {
		return org.clone(), true
	}
	domain, ok := strings.CutPrefix(key, "d:")
	if !ok || domain == "" || b.shared[domain] || b.domain[domain] != "" || b.domains.Of("x@"+domain) != domain {
		return CompanyOrg{}, false
	}
	return CompanyOrg{Key: key, Label: domain, Domains: []string{domain},
		Addresses: []string{}, People: []CompanyPerson{}}, true
}

// StructurallySharedDomains returns the domains the address book shows as
// shared by several unrelated declared companies, sorted. They are not an
// organization even when the learned shared-domain setting has not caught
// them yet; exact company declarations still resolve.
func (b *CompanyBook) StructurallySharedDomains() []string {
	out := make([]string, 0, len(b.shared))
	for domain := range b.shared {
		out = append(out, domain)
	}
	sort.Strings(out)
	return out
}

// List returns every organization the visible address books know, sorted by
// folded label then key. It includes the owner's internal organizations;
// callers that rank or show external companies filter with IsInternal (or
// read Coverage.OwnerDomains / OwnerCompanies).
func (b *CompanyBook) List() []CompanyOrg {
	out := make([]CompanyOrg, 0, len(b.orgs))
	for _, org := range b.orgs {
		out = append(out, org.clone())
	}
	sort.Slice(out, func(i, j int) bool {
		li, lj := mail.Fold(out[i].Label), mail.Fold(out[j].Label)
		if li != lj {
			return li < lj
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// IsInternal reports whether an address belongs to the owner: one of the
// owner domains, or a declared company that owner-domain contacts declare
// (even on a shared free-mail domain).
func (b *CompanyBook) IsInternal(address string) bool {
	resolved, ok := b.resolve(address)
	return ok && b.isInternalResolution(resolved)
}

// isInternalResolution is the one internal rule over an already-resolved
// address, so the company ranking and the profile do not resolve twice.
func (b *CompanyBook) isInternalResolution(resolved companyResolution) bool {
	if resolved.domain != "" && b.ownerDomains[resolved.domain] {
		return true
	}
	return resolved.org.Declared && b.ownerCompanies[resolved.org.Key]
}

// Coverage reports how much of the visible address book became organizations.
func (b *CompanyBook) Coverage() CompanyCoverage { return b.coverage }

// matchesOrg reports whether an address names the organization. An address
// that resolves to another organization never matches by domain: two declared
// companies can share a corporate domain without being the same organization,
// so the resolution is authoritative. Only an address the book cannot resolve
// (an unknown colleague on one of the organization's non-shared domains)
// falls back to the domain list. Calendar and deadline evidence use this one
// predicate.
func (b *CompanyBook) matchesOrg(org CompanyOrg, address string) bool {
	address = mail.NormalizeAddress(address)
	if address == "" {
		return false
	}
	if resolved, ok := b.resolve(address); ok {
		return resolved.org.Key == org.Key
	}
	domain := b.domains.Of(address)
	if domain == "" {
		return false
	}
	return slices.Contains(org.Domains, domain)
}

// addCompanySpelling counts one declared spelling for an address or domain.
func addCompanySpelling(counts map[string]map[string]int, key, spelling string) {
	if counts[key] == nil {
		counts[key] = map[string]int{}
	}
	counts[key][spelling]++
}

// dominantCompanySpelling is the most frequent spelling, with a folded
// alphabetical tie-break so the label is stable across runs.
func dominantCompanySpelling(counts map[string]int) string {
	best, bestN := "", 0
	for spelling, n := range counts {
		if n > bestN || (n == bestN && companySpellingLess(spelling, best)) {
			best, bestN = spelling, n
		}
	}
	return best
}

func companySpellingLess(a, b string) bool {
	fa, fb := mail.Fold(a), mail.Fold(b)
	if fa != fb {
		return fa < fb
	}
	return a < b
}

// messageIdentity dedupes one stored message by source, store and source
// message id, falling back to the row id when no source id was reported.
func messageIdentity(sourceID, storeID, msgID string, messageID int64) string {
	if msgID != "" {
		return sourceID + "\x00" + storeID + "\x00" + msgID
	}
	return fmt.Sprintf("m:%d", messageID)
}

// sortedUniqueStrings returns a sorted, de-duplicated copy of in. It never
// mutates the caller's slice.
func sortedUniqueStrings(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	return slices.Compact(slices.Sorted(slices.Values(in)))
}
