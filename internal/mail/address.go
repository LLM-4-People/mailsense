package mail

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// legacyDNRE recognises Exchange legacy distinguished names (/o=…, /ou=…, /cn=…).
var legacyDNRE = regexp.MustCompile(`(?i)^/(o|ou|cn)=`)

var addressSplitRE = regexp.MustCompile(`[;,]`)

// MaxSenderQueryRunes bounds one sender address or domain filter. It is the
// one bound of the HTTP surface and the store, so search and All mail agree.
const MaxSenderQueryRunes = 320

// HasControl reports a control character in a value that reaches an address,
// filter or event field. unicode.IsControl is the one rule, covering C0, DEL and
// the C1 range: an ASCII-only check would let U+0080..U+009F through in one path
// while another rejects them.
func HasControl(s string) bool { return strings.ContainsFunc(s, unicode.IsControl) }

// ErrInvalidSenderQuery is returned for an empty, over-long or control-bearing
// sender filter. The store and the API both own a typed error of their own and
// wrap this one; the wire code stays invalid_sender.
var ErrInvalidSenderQuery = errors.New("mail: invalid sender query")

// NormalizeAddress is the one comparison form of an address: trimmed and
// lower-cased (Go's ToLower is locale-independent, unlike .NET's).
func NormalizeAddress(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// AddressDomain is the exact-domain key of a normalized address: the text
// after its last '@', "" when there is none. A subdomain is a different
// domain, so a domain filter never widens to it.
func AddressDomain(address string) string {
	if i := strings.LastIndexByte(address, '@'); i >= 0 {
		return address[i+1:]
	}
	return ""
}

// NormalizeSenderQuery validates one sender filter and returns its canonical
// comparison form. The value is either an exact address or a domain (with a
// leading @, or without one); the store derives which. A lone @ has an empty
// domain and is refused. Unicode lower-casing is Go's: SQLite's lower() is
// ASCII-only, so callers must compare with the mailsense_lower scalar.
func NormalizeSenderQuery(s string) (string, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return "", fmt.Errorf("%w: empty", ErrInvalidSenderQuery)
	case utf8.RuneCountInString(s) > MaxSenderQueryRunes:
		return "", fmt.Errorf("%w: longer than %d runes", ErrInvalidSenderQuery, MaxSenderQueryRunes)
	case strings.ContainsFunc(s, unicode.IsControl):
		return "", fmt.Errorf("%w: control character", ErrInvalidSenderQuery)
	}
	canonical := NormalizeAddress(s)
	if canonical == "@" {
		return "", fmt.Errorf("%w: empty domain", ErrInvalidSenderQuery)
	}
	return canonical, nil
}

// IsLegacyDN reports whether s is an Exchange legacy DN rather than SMTP.
func IsLegacyDN(s string) bool { return legacyDNRE.MatchString(strings.TrimSpace(s)) }

// Domain returns the normalized domain of an SMTP address, or "".
func Domain(address string) string {
	a := NormalizeAddress(address)
	if i := strings.LastIndexByte(a, '@'); i >= 0 {
		return a[i+1:]
	}
	return ""
}

// SplitAddresses splits a joined address list on ',' or ';', trimming each
// part and dropping empty ones; order and case are preserved.
func SplitAddresses(s string) []string {
	var out []string
	for _, part := range addressSplitRE.Split(s, -1) {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
