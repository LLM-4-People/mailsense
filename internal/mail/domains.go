package mail

import (
	"slices"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/psl"
)

// RegistrableDomain is the registrable domain of an address (the public
// suffix plus one label, Public Suffix List), in ASCII form; "" without
// a domain.
func RegistrableDomain(address string) string {
	d := Domain(address)
	if d == "" {
		return ""
	}
	return psl.Registrable(d)
}

// RegistrableName is the registrable form of a bare domain (no local part),
// the form an organization's domain column holds. RegistrableDomain expects
// an address, so comparing a person's address with a company domain needs
// this one conversion; "" for an empty or invalid name.
func RegistrableName(domain string) string {
	d := strings.ToLower(strings.TrimSpace(domain))
	d = strings.TrimPrefix(d, "@")
	if d == "" || strings.ContainsAny(d, " \t\r\n") {
		return ""
	}
	return psl.Registrable(d)
}

// Domains groups senders by organisation: the registrable domain of an
// address, except for domains this install learned are shared by
// unrelated people (free mail, ISPs: mail.domains.shared), which group
// nothing. The one grouping rule of priors, few-shot labels, suspicious
// labels and insights.
type Domains struct{ shared []string }

// NewDomains builds the grouping from the shared domains (any spelling).
func NewDomains(shared []string) Domains {
	var out []string
	for _, d := range shared {
		if r := psl.Registrable(d); r != "" && !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	slices.Sort(out)
	return Domains{shared: out}
}

// Of is the organisation key of an address: its registrable domain, or
// "" for a shared domain or an address without a domain.
func (d Domains) Of(address string) string {
	r := RegistrableDomain(address)
	if r == "" || d.Shared(r) {
		return ""
	}
	return r
}

// Shared reports whether a registrable domain is a shared one.
func (d Domains) Shared(registrable string) bool {
	_, found := slices.BinarySearch(d.shared, registrable)
	return found
}
