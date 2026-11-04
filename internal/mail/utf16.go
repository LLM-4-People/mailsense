package mail

import "unicode/utf16"

// UTF16Len is the length of s in UTF-16 code units: the unit Outlook
// (.NET/COM strings) counts characters in, so caps match the source's.
func UTF16Len(s string) int {
	n := 0
	for _, r := range s {
		n += units(r)
	}
	return n
}

// TruncateUTF16 cuts s to at most n UTF-16 code units without splitting a
// character and reports whether anything was cut. n < 0 counts as 0.
func TruncateUTF16(s string, n int) (string, bool) {
	used := 0
	for i, r := range s {
		if used += units(r); used > n {
			return s[:i], true
		}
	}
	return s, false
}

// units is the UTF-16 length of one decoded rune (an invalid rune is
// encoded as U+FFFD, one unit).
func units(r rune) int {
	if n := utf16.RuneLen(r); n > 0 {
		return n
	}
	return 1
}
