package evidence

import (
	"regexp"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// ReasonNoSenderDomain: the sender's address has no domain (an Exchange
// distinguished name): nothing to compare a domain with.
const ReasonNoSenderDomain = "sender_without_domain"

var addressToken = regexp.MustCompile(`[^\s<>,;:"'()\[\]]+@[^\s<>,;:"'()\[\]]+\.[^\s<>,;:"'()\[\]]+`)

// addressesIn are the address-shaped tokens of a header value or name.
func addressesIn(s string) []string { return addressToken.FindAllString(s, -1) }

// relation compares an address with the sender's: same_address,
// same_domain (registrable domain) or other_domain.
func relation(addr, sender string) string {
	switch {
	case mail.NormalizeAddress(addr) == mail.NormalizeAddress(sender):
		return "same_address"
	case mail.RegistrableDomain(addr) == mail.RegistrableDomain(sender):
		return "same_domain"
	}
	return "other_domain"
}
