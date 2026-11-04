package psl

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// Punycode (RFC 3492) encoding of one label, for the ASCII form of
// internationalized domain names: the list's Unicode rules and an
// address's Unicode domain both match in their "xn--" form.
const (
	pcBase        = 36
	pcTMin        = 1
	pcTMax        = 26
	pcSkew        = 38
	pcDamp        = 700
	pcInitialBias = 72
	pcInitialN    = 128
	acePrefix     = "xn--"
)

var errPunycode = errors.New("psl: label cannot be encoded")

// toASCII is the ASCII form of one lower-case label.
func toASCII(label string) (string, error) {
	for i := 0; i < len(label); i++ {
		if label[i] >= utf8.RuneSelf {
			enc, err := punyEncode(label)
			if err != nil {
				return "", err
			}
			return acePrefix + enc, nil
		}
	}
	return label, nil
}

func punyEncode(s string) (string, error) {
	runes := []rune(s)
	var out strings.Builder
	basic := 0
	for _, r := range runes {
		if r < 0x80 {
			out.WriteRune(r)
			basic++
		}
	}
	h := basic
	if basic > 0 {
		out.WriteByte('-')
	}
	n, delta, bias := rune(pcInitialN), 0, pcInitialBias
	for h < len(runes) {
		m := rune(0x7fffffff)
		for _, r := range runes {
			if r >= n && r < m {
				m = r
			}
		}
		if int(m-n) > (1<<31-1-delta)/(h+1) {
			return "", errPunycode
		}
		delta += int(m-n) * (h + 1)
		n = m
		for _, r := range runes {
			if r < n {
				delta++
			}
			if r != n {
				continue
			}
			q := delta
			for k := pcBase; ; k += pcBase {
				t := k - bias
				t = min(max(t, pcTMin), pcTMax)
				if q < t {
					break
				}
				out.WriteByte(digit(t + (q-t)%(pcBase-t)))
				q = (q - t) / (pcBase - t)
			}
			out.WriteByte(digit(q))
			bias = adapt(delta, h+1, h == basic)
			delta = 0
			h++
		}
		delta++
		n++
	}
	return out.String(), nil
}

func digit(d int) byte {
	if d < 26 {
		return byte('a' + d)
	}
	return byte('0' + d - 26)
}

func adapt(delta, points int, first bool) int {
	if first {
		delta /= pcDamp
	} else {
		delta /= 2
	}
	delta += delta / points
	k := 0
	for delta > ((pcBase-pcTMin)*pcTMax)/2 {
		delta /= pcBase - pcTMin
		k += pcBase
	}
	return k + (pcBase-pcTMin+1)*delta/(delta+pcSkew)
}
