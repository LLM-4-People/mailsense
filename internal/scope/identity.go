// Package scope decides whose mail each message is (PORTING §3): the
// owner's own traffic, someone else's (shared alias/mailbox), or
// unattributable. A mailbox store's owner is never guessed: it is the
// addresses its source reports for the picked mailbox plus the addresses
// the user declares (source.SettingDeclared). It owns the ONE owner
// identity of the codebase.
package scope

import (
	"slices"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/psl"
)

// Identity is the owner of one mailbox store: normalized addresses, the
// primary one first, then aliases (SMTP proxies, the Exchange DN,
// declared addresses). Subaddresses (local+tag@domain, RFC 5233) of an
// owned address are owned too, for the configured separators.
type Identity struct {
	Addresses  []string `json:"addresses"`
	Separators []string `json:"-"` // scope.subaddress_separators
}

// NewIdentity joins reported and declared addresses (canonical form,
// de-duplicated, order kept: reported first). It is the one place owner
// addresses are normalized: sources and settings hand them over as given.
func NewIdentity(separators []string, lists ...[]string) Identity {
	id := Identity{Separators: separators}
	for _, list := range lists {
		for _, a := range list {
			if n := canonical(a); n != "" && !slices.Contains(id.Addresses, n) {
				id.Addresses = append(id.Addresses, n)
			}
		}
	}
	return id
}

// Known reports whether the store has an owner identity at all.
func (id Identity) Known() bool { return len(id.Addresses) > 0 }

// Digest names an identity: its normalized addresses and subaddress rule.
// It is what a stored verdict records as the identity it judged, so any
// change re-judges the store's mail. The output is persisted in
// scope_verdicts.identity: keep it byte-identical when changing this.
func (id Identity) Digest() string {
	return digest.Hex([]byte(strings.Join(id.Addresses, "\n")+"\x00"+strings.Join(id.Separators, "\n")), 16)
}

// Primary is the first (primary) address, "" when unknown.
func (id Identity) Primary() string {
	if !id.Known() {
		return ""
	}
	return id.Addresses[0]
}

// Owns reports whether address is one of the owner's addresses or a
// subaddress of one.
func (id Identity) Owns(address string) bool {
	a := canonical(address)
	if a == "" {
		return false
	}
	if slices.Contains(id.Addresses, a) {
		return true
	}
	local, domain, ok := cutLast(a, "@")
	if !ok {
		return false
	}
	for _, sep := range id.Separators {
		if base, _, cut := strings.Cut(local, sep); cut && base != "" && slices.Contains(id.Addresses, base+"@"+domain) {
			return true
		}
	}
	return false
}

// canonical is the form identities compare: lower case, the domain in
// ASCII ("xn--") form, so the Unicode and the Punycode spelling of an IDN
// address are one address.
func canonical(address string) string {
	a := mail.NormalizeAddress(address)
	local, domain, ok := cutLast(a, "@")
	if !ok || isASCII(domain) {
		return a
	}
	if d, ok := psl.ASCII(domain); ok {
		return local + "@" + d
	}
	return a
}

func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func cutLast(s, sep string) (string, string, bool) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+len(sep):], true
}
