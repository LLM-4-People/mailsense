package locale

import (
	"strings"
	"sync"

	"golang.org/x/text/unicode/norm"
)

var confusableMap = sync.OnceValue(func() map[rune]string {
	out := map[rune]string{}
	for src, dst := range confusablesData().Map {
		r := []rune(src)
		if len(r) != 1 {
			panic("locale: confusables.json: " + src + " is not one character")
		}
		out[r[0]] = dst
	}
	return out
})

// Skeleton is the UTS #39 skeleton of s: NFD, every character replaced by
// its confusables prototype, NFD again. Two strings are confusable when
// their skeletons are equal (раураl and paypal).
func Skeleton(s string) string {
	m := confusableMap()
	var b strings.Builder
	for _, r := range norm.NFD.String(s) {
		if p, ok := m[r]; ok {
			b.WriteString(p)
			continue
		}
		b.WriteRune(r)
	}
	return norm.NFD.String(b.String())
}
