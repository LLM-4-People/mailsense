package main

import (
	"bufio"
	"os"
	"strconv"
	"strings"
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

// codeRange parses "0041" or "0041..005A".
func codeRange(s string) (lo, hi rune, ok bool) {
	a, b, isRange := strings.Cut(s, "..")
	x, err := strconv.ParseUint(a, 16, 32)
	if err != nil {
		return 0, 0, false
	}
	if !isRange {
		return rune(x), rune(x), true
	}
	y, err := strconv.ParseUint(b, 16, 32)
	return rune(x), rune(y), err == nil
}

// property reads the code points of one property value of a file whose
// records are "<range> ; <value>" (PropList.txt, Scripts.txt).
func property(path, value string) (map[rune]bool, error) {
	out := map[rune]bool{}
	err := eachRecord(path, func(f []string) {
		if len(f) < 2 || f[1] != value {
			return
		}
		if lo, hi, ok := codeRange(f[0]); ok {
			for r := lo; r <= hi; r++ {
				out[r] = true
			}
		}
	})
	return out, err
}

// char is one UnicodeData.txt record.
type char struct {
	name, category, decomposition string
}

func unicodeChars(path string) (map[rune]char, error) {
	out := map[rune]char{}
	err := eachRecord(path, func(f []string) {
		if len(f) < 6 {
			return
		}
		if r, _, ok := codeRange(f[0]); ok {
			out[r] = char{name: f[1], category: f[2], decomposition: f[5]}
		}
	})
	return out, err
}
