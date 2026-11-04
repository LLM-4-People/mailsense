// Package contacts holds the address-book entities the world model links to
// mail and calendar. It is pure: a connector fills Contact, the store keeps
// it, and the agenda matches people by normalized address.
package contacts

import (
	"strings"
	"unicode"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// Contact is one address-book entry as a source reports it. A contact may
// carry several addresses (Outlook Email1/2/3, Exchange aliases); the first
// is the primary one.
type Contact struct {
	SourceID     string   `json:"source_id"`
	StoreID      string   `json:"store_id"`
	StoreKey     string   `json:"store_key"`
	EntryID      string   `json:"entry_id"`
	Revision     string   `json:"revision"`
	DisplayName  string   `json:"display_name"`
	Company      string   `json:"company"`
	Department   string   `json:"department"`
	JobTitle     string   `json:"job_title"`
	FolderPath   string   `json:"folder_path"`
	LastModified string   `json:"last_modified"`
	Phone        string   `json:"phone,omitempty"` // display form, empty when the source has none
	Emails       []string `json:"emails"`
}

// maxPhoneRunes bounds a source phone value for display. It is not a
// validation rule: extensions and local formatting stay as the source wrote
// them, and an over-long value is cut rather than rejected.
const maxPhoneRunes = 64

// CleanPhone trims a source phone value and removes control characters. It
// never reformats the digits, so an international or extension-bearing value
// stays readable exactly as the address book reports it.
func CleanPhone(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(value))
	if runes := []rune(value); len(runes) > maxPhoneRunes {
		value = string(runes[:maxPhoneRunes])
	}
	return strings.TrimSpace(value)
}

// Normalize canonicalizes an address for matching: trimmed, lowercased, and
// with a leading "mailto:" removed. It never invents an address.
func Normalize(address string) string {
	a := strings.TrimSpace(address)
	if len(a) > 7 && strings.EqualFold(a[:7], "mailto:") {
		a = a[7:]
	}
	return strings.ToLower(strings.TrimSpace(a))
}

// Valid reports whether an address is worth storing: one "@", a non-empty
// local part and domain, and no whitespace or control characters.
func Valid(address string) bool {
	a := Normalize(address)
	if a == "" || strings.ContainsFunc(a, unicode.IsSpace) || mail.HasControl(a) {
		return false
	}
	local, domain, ok := strings.Cut(a, "@")
	return ok && local != "" && domain != "" && !strings.Contains(domain, "@")
}

// Name returns the label to show for a contact: its display name, else the
// primary address, else the first valid address.
func (c Contact) Name() string {
	if n := strings.TrimSpace(c.DisplayName); n != "" {
		return n
	}
	if len(c.Emails) > 0 {
		return Normalize(c.Emails[0])
	}
	return ""
}

// Primary returns the first valid address, normalized ("" when none).
func (c Contact) Primary() string {
	for _, e := range c.Emails {
		if Valid(e) {
			return Normalize(e)
		}
	}
	return ""
}

// Addresses returns the valid addresses, normalized and de-duplicated in
// source order. It is the only form the store writes.
func (c Contact) Addresses() []string {
	out := make([]string, 0, len(c.Emails))
	seen := map[string]bool{}
	for _, e := range c.Emails {
		n := Normalize(e)
		if Valid(n) && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}
