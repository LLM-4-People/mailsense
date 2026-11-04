package main

import (
	"fmt"
	"strconv"
	"strings"
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
		src, ok1 := codePoints(f[0])
		dst, ok2 := codePoints(f[1])
		if !ok1 || !ok2 || len([]rune(src)) != 1 {
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

// codePoints parses space-separated hex code points.
func codePoints(s string) (string, bool) {
	var b strings.Builder
	for _, h := range strings.Fields(strings.TrimPrefix(s, "\ufeff")) {
		v, err := strconv.ParseUint(h, 16, 32)
		if err != nil {
			return "", false
		}
		b.WriteRune(rune(v))
	}
	return b.String(), b.Len() > 0
}
