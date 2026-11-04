package psl

import (
	"strings"
)

// Unicode is the Unicode form of an ASCII host name: every "xn--" label
// decoded (RFC 3492); ok is false when a label does not decode (it is
// not a valid A-label). Look-alike checks compare this form, the one a
// reader sees.
func Unicode(host string) (string, bool) {
	labels := strings.Split(host, ".")
	for i, l := range labels {
		if !strings.HasPrefix(l, acePrefix) {
			continue
		}
		u, ok := punyDecode(l[len(acePrefix):])
		if !ok {
			return "", false
		}
		labels[i] = u
	}
	return strings.Join(labels, "."), true
}

// punyDecode is the RFC 3492 §6.2 decoding procedure.
func punyDecode(s string) (string, bool) {
	var out []rune
	if i := strings.LastIndexByte(s, '-'); i >= 0 {
		for _, r := range s[:i] {
			if r >= 0x80 {
				return "", false
			}
			out = append(out, r)
		}
		s = s[i+1:]
	}
	n, bias, i := rune(pcInitialN), pcInitialBias, 0
	for pos := 0; pos < len(s); {
		old, w := i, 1
		for k := pcBase; ; k += pcBase {
			if pos >= len(s) {
				return "", false
			}
			d, ok := digitValue(s[pos])
			pos++
			if !ok || d > (1<<31-1-i)/w {
				return "", false
			}
			i += d * w
			t := min(max(k-bias, pcTMin), pcTMax)
			if d < t {
				break
			}
			w *= pcBase - t
		}
		bias = adapt(i-old, len(out)+1, old == 0)
		n += rune(i / (len(out) + 1))
		i %= len(out) + 1
		out = append(out[:i], append([]rune{n}, out[i:]...)...)
		i++
	}
	return string(out), len(out) > 0
}

func digitValue(c byte) (int, bool) {
	switch {
	case c >= 'a' && c <= 'z':
		return int(c - 'a'), true
	case c >= 'A' && c <= 'Z':
		return int(c - 'A'), true
	case c >= '0' && c <= '9':
		return int(c-'0') + 26, true
	}
	return 0, false
}
