package main

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// confusables reads UTS #39 confusables.txt: "source ; target ; type"
// with code points in hex; every source is one character.
func confusables(path string) (map[string]string, error) {
	out := map[string]string{}
	var bad []string
	err := eachRecord(path, func(f []string) {
		if len(f) < 2 {
			bad = append(bad, strings.Join(f, ";"))
			return
		}
		src, srcErr := codePoints(f[0])
		dst, dstErr := codePoints(f[1])
		if srcErr != nil {
			bad = append(bad, fmt.Sprintf("%s: %v", f[0], srcErr))
			return
		}
		if dstErr != nil {
			bad = append(bad, fmt.Sprintf("%s: %v", f[1], dstErr))
			return
		}
		if len([]rune(src)) != 1 {
			bad = append(bad, f[0])
			return
		}
		out[src] = dst
	})
	if err == nil && (len(bad) > 0 || len(out) == 0) {
		err = fmt.Errorf("%s: %d unreadable records (first %v), %d read", path, len(bad), bad[:min(1, len(bad))], len(out))
	}
	return out, err
}

// codePoints parses space-separated hex code points. A value that is not a
// Unicode scalar value is an error: rune(v) would silently truncate it.
func codePoints(s string) (string, error) {
	var b strings.Builder
	for _, h := range strings.Fields(strings.TrimPrefix(s, "\ufeff")) {
		v, err := strconv.ParseUint(h, 16, 32)
		if err != nil {
			return "", fmt.Errorf("code point %q: %w", h, err)
		}
		if v > unicode.MaxRune || (v >= 0xD800 && v <= 0xDFFF) {
			return "", fmt.Errorf("code point %q is not a Unicode scalar value", h)
		}
		b.WriteRune(rune(v))
	}
	if b.Len() == 0 {
		return "", fmt.Errorf("no code points")
	}
	return b.String(), nil
}
