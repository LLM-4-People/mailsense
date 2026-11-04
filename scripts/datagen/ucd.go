package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode"
)

// eachRecord calls fn with the semicolon-separated, trimmed fields of
// every data line of a UCD-format file (comments after '#' removed).
func eachRecord(path string, fn func(fields []string)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<20)
	for sc.Scan() {
		line, _, _ := strings.Cut(sc.Text(), "#")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, ";")
		for i := range fields {
			fields[i] = strings.TrimSpace(fields[i])
		}
		fn(fields)
	}
	return sc.Err()
}

// codeRange parses "0041" or "0041..005A". A value that is not a Unicode
// scalar value is an error, not a truncated rune.
func codeRange(s string) (lo, hi rune, err error) {
	a, b, isRange := strings.Cut(s, "..")
	x, err := parseCodePoint(a)
	if err != nil {
		return 0, 0, err
	}
	if !isRange {
		return x, x, nil
	}
	y, err := parseCodePoint(b)
	if err != nil {
		return 0, 0, err
	}
	if y < x {
		return 0, 0, fmt.Errorf("range %q is reversed", s)
	}
	return x, y, nil
}

// parseCodePoint parses one hexadecimal code point and refuses anything
// outside the Unicode scalar values (past U+10FFFF or a surrogate), which
// rune(v) would otherwise truncate.
func parseCodePoint(s string) (rune, error) {
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return 0, fmt.Errorf("code point %q: %w", s, err)
	}
	if v > unicode.MaxRune || (v >= 0xD800 && v <= 0xDFFF) {
		return 0, fmt.Errorf("code point %q is not a Unicode scalar value", s)
	}
	return rune(v), nil
}

// property reads the code points of one property value of a file whose
// records are "<range> ; <value>" (PropList.txt, Scripts.txt).
func property(path, value string) (map[rune]bool, error) {
	out := map[rune]bool{}
	var bad error
	err := eachRecord(path, func(f []string) {
		if bad != nil || len(f) < 2 || f[1] != value {
			return
		}
		lo, hi, err := codeRange(f[0])
		if err != nil {
			bad = fmt.Errorf("%s: %w", path, err)
			return
		}
		for r := lo; r <= hi; r++ {
			out[r] = true
		}
	})
	if err != nil {
		return out, err
	}
	return out, bad
}

// char is one UnicodeData.txt record.
type char struct {
	name, category, decomposition string
}

func unicodeChars(path string) (map[rune]char, error) {
	out := map[rune]char{}
	var bad error
	err := eachRecord(path, func(f []string) {
		if bad != nil || len(f) < 6 {
			return
		}
		r, _, err := codeRange(f[0])
		if err != nil {
			bad = fmt.Errorf("%s: %w", path, err)
			return
		}
		out[r] = char{name: f[1], category: f[2], decomposition: f[5]}
	})
	if err != nil {
		return out, err
	}
	return out, bad
}
