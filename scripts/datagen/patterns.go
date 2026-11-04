package main

import (
	"slices"
	"strings"
)

// addPatterns fills the chosen lengths not set yet.
func addPatterns(dst map[string]string, ls []length, get func(length) []string, lengths []string) {
	for _, l := range ls {
		key := "length:" + l.Type
		if !slices.Contains(lengths, l.Type) || dst[key] != "" {
			continue
		}
		for _, v := range get(l) {
			if v = strings.TrimSpace(v); v != "" && v != inherit {
				dst[key] = v
				break
			}
		}
	}
}

// Pattern fields (UTS #35 date field symbols): the ones the matcher
// reads, the zone fields that are dropped, everything else unsupported.
const (
	readFields = "yMLdEcabBhHkKms"
	zoneFields = "zZOvVxX"
)

// shippable lists the distinct patterns, zone fields dropped, without
// the ones holding a field the matcher does not read, sorted.
func shippable(set map[string]string) []string {
	var out []string
	for _, p := range set {
		p = dropZones(p)
		if p == "" || !onlyReadFields(p) || slices.Contains(out, p) {
			continue
		}
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// dropZones removes zone fields and the spaces around them.
func dropZones(p string) string {
	var b strings.Builder
	in := false
	for _, r := range p {
		if r == '\'' {
			in = !in
		}
		if !in && strings.ContainsRune(zoneFields, r) {
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

func onlyReadFields(p string) bool {
	in := false
	for _, r := range p {
		switch {
		case r == '\'':
			in = !in
		case in:
		case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z':
			if !strings.ContainsRune(readFields, r) {
				return false
			}
		}
	}
	return true
}
