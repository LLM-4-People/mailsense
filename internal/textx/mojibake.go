// Package textx holds small text normalizations shared by the store and the
// services. They are deliberately dependency-free and deterministic so the
// same input always produces the same output.
package textx

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// maxSequence is the longest run of Windows-1252 characters one UTF-8
// character can turn into: the three-byte dash and quote forms (â€™, â€œ,
// â€”) are the longest. A longer window would swallow the following text.
const maxSequence = 4

// cp1252Rune reports whether r is a printable Windows-1252 character. It is
// the alphabet a UTF-8 byte sequence turns into when it is decoded as
// Windows-1252 by a legacy reader (Outlook export, PowerShell 5.1 console,
// ISO-8859-1 database driver). ASCII and control characters are excluded:
// they cannot carry a mangled sequence.
func cp1252Rune(r rune) bool {
	if r >= 0x00A0 && r <= 0x00FF {
		return true
	}
	switch r {
	case 0x0152, 0x0153, 0x0160, 0x0161, 0x0178, 0x017D, 0x017E, // Œ œ Š š Ÿ Ž ž
		0x0192, 0x02C6, 0x02DC, // ƒ ˆ ˜
		0x2013, 0x2014, 0x2018, 0x2019, 0x201A, // – — ‘ ’ ‚
		0x201C, 0x201D, 0x201E, 0x2020, 0x2021, 0x2022, // “ ” „ † ‡ •
		0x2026, 0x2030, 0x2039, 0x203A, 0x20AC, 0x2122: // … ‰ ‹ › € ™
		return true
	}
	return false
}

// isLead reports whether r can start a mangled UTF-8 sequence. Every lead
// byte of a two- or three-byte UTF-8 character falls in this set when it is
// read as Windows-1252: 0xC2/0xC3 for Latin-1 letters and punctuation,
// 0xE2 for the general-punctuation block (dashes, curly quotes, ellipsis).
func isLead(r rune) bool { return r == 0x00C2 || r == 0x00C3 || r == 0x00E2 }

// markers counts the lead characters of a mangled sequence. A repair must
// remove at least one; this is what stops a round trip that would leave the
// text as mangled as it found it.
func markers(s string) int {
	n := 0
	for _, r := range s {
		if isLead(r) {
			n++
		}
	}
	return n
}

var cp1252 = charmap.Windows1252.NewEncoder()

// Repair decodes the unambiguous UTF-8-read-as-Windows-1252 sequences in s
// (for example "RÃ©sume" to "Résumé") and reports how many sequences it
// repaired. Ordinary accented text ("café", "déjà") is never touched: one
// Windows-1252 byte is not valid UTF-8, so it cannot be a mangled sequence.
// A sequence is repaired only when the round trip yields valid UTF-8, changes
// the text, and removes a lead character; anything else is left verbatim.
// Up to three passes undo repeated encoding. Repair is idempotent.
func Repair(s string) (string, int) {
	if s == "" || !utf8.ValidString(s) || !strings.ContainsAny(s, "\u00c2\u00c3\u00e2") {
		return s, 0
	}
	total := 0
	for pass := 0; pass < 3; pass++ {
		out, n := repairPass(s)
		if n == 0 {
			break
		}
		s, total = out, total+n
	}
	return s, total
}

// repairPass walks the string once, decoding every mangled sequence it finds.
func repairPass(s string) (string, int) {
	runes := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	repaired := 0
	for i := 0; i < len(runes); {
		if !isLead(runes[i]) {
			b.WriteRune(runes[i])
			i++
			continue
		}
		decoded, used, ok := decodeAt(runes[i:])
		if !ok {
			b.WriteRune(runes[i])
			i++
			continue
		}
		b.WriteString(decoded)
		i += used
		repaired++
	}
	if repaired == 0 {
		return s, 0
	}
	return b.String(), repaired
}

// decodeAt decodes the longest prefix of runes that is a Windows-1252
// spelling of valid UTF-8. It returns the decoded text and how many runes it
// consumed.
func decodeAt(runes []rune) (string, int, bool) {
	limit := min(maxSequence, len(runes))
	for take := limit; take >= 2; take-- {
		window := runes[:take]
		representable := true
		for _, r := range window {
			if !cp1252Rune(r) {
				representable = false
				break
			}
		}
		if !representable {
			continue
		}
		raw, err := cp1252.Bytes([]byte(string(window)))
		if err != nil || !utf8.Valid(raw) {
			continue
		}
		decoded := string(raw)
		if decoded == string(window) || markers(decoded) >= markers(string(window)) {
			continue
		}
		return decoded, take, true
	}
	return "", 0, false
}
