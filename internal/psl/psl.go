// Package psl answers what the registrable domain of a host is (the
// public suffix plus one label: "example.co.uk" for "mail.example.co.uk",
// "gmail.com" for "gmail.com"), from the Public Suffix List shipped as
// data (public_suffix_list.dat, pinned in pin.json, updated only by
// scripts/update-psl.sh). Hosts and rules compare in their ASCII ("xn--")
// form, so a Unicode and a Punycode spelling of one domain are one domain.
package psl

import (
	_ "embed"
	"net/netip"
	"strings"
	"sync"

	"golang.org/x/text/unicode/norm"

	"github.com/LLM-4-People/mailsense/internal/digest"
)

//go:embed public_suffix_list.dat
var listData string

type ruleKind uint8

const (
	ruleNormal ruleKind = iota + 1
	ruleWildcard
	ruleException
)

// list is the parsed list, built once (immutable): its rules by ASCII
// suffix, and the rules that could not be read (a data defect: a test
// requires none).
type list struct {
	rules    map[string]ruleKind
	unusable []string
}

var shipped = sync.OnceValue(func() *list { return parse(listData) })

func parse(data string) *list {
	l := &list{rules: map[string]ruleKind{}}
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		line, _, _ = strings.Cut(line, " ")
		kind, rule := ruleNormal, line
		switch {
		case strings.HasPrefix(rule, "!"):
			kind, rule = ruleException, rule[1:]
		case strings.HasPrefix(rule, "*."):
			kind, rule = ruleWildcard, rule[2:]
		}
		ascii, ok := ASCII(rule)
		if !ok {
			l.unusable = append(l.unusable, line)
			continue
		}
		l.rules[ascii] = kind
	}
	return l
}

// Unusable lists the rules of the shipped list that could not be read
// (none, or the data is defective: TestTheShippedListIsThePinnedOne).
func Unusable() []string { return append([]string(nil), shipped().unusable...) }

// ideographicStop is the one label separator besides "." left after NFKC
// (RFC 3490 §3.1 / UTS #46: U+3002, U+FF0E and U+FF61 separate labels;
// NFKC maps U+FF0E to "." and U+FF61 to U+3002).
const ideographicStop = "\u3002"

// notInHosts are characters no host name holds (address, URL and
// literal syntax, white space).
const notInHosts = " \t\r\n[]@:/\\<>"

// ASCII lower-cases and folds a host (NFKC, ideographic full stops as
// dots, a trailing dot dropped) and gives it in ASCII ("xn--") form;
// false when it is not a host name.
func ASCII(host string) (string, bool) {
	host = strings.ReplaceAll(norm.NFKC.String(strings.TrimSpace(host)), ideographicStop, ".")
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" || strings.ContainsAny(host, notInHosts) {
		return "", false
	}
	labels := strings.Split(host, ".")
	for i, l := range labels {
		if l == "" {
			return "", false
		}
		a, err := toASCII(l)
		if err != nil {
			return "", false
		}
		labels[i] = a
	}
	return strings.Join(labels, "."), true
}

// Registrable returns the registrable domain of host in ASCII form, or
// host itself (ASCII form) when host is a public suffix or has a single
// label, or "" when host is not a host name. An IP address literal
// (192.0.2.1, [192.0.2.1], [IPv6:2001:db8::1]) has no public suffix: it
// is its own organisation key, returned as given (lower case).
func Registrable(host string) string {
	if ip, ok := ipLiteral(host); ok {
		return ip
	}
	h, ok := ASCII(host)
	if !ok {
		return ""
	}
	labels := strings.Split(h, ".")
	n := publicSuffixLabels(shipped().rules, labels)
	if n >= len(labels) {
		return h
	}
	return strings.Join(labels[len(labels)-n-1:], ".")
}

// ipLiteral recognises an address literal (RFC 5321 §4.1.3, bracketed or
// bare) and returns it lower-cased.
func ipLiteral(host string) (string, bool) {
	h := strings.ToLower(strings.TrimSpace(host))
	inner := h
	if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		inner = strings.TrimPrefix(h[1:len(h)-1], "ipv6:")
	}
	if _, err := netip.ParseAddr(inner); err != nil {
		return "", false
	}
	return h, true
}

// publicSuffixLabels is how many trailing labels form the public suffix
// (PSL algorithm: an exception rule prevails, else the longest rule,
// else "*").
func publicSuffixLabels(rules map[string]ruleKind, labels []string) int {
	best := 1 // the default rule "*"
	for i := range labels {
		suffix := strings.Join(labels[i:], ".")
		switch rules[suffix] {
		case ruleException:
			return len(labels) - i - 1
		case ruleNormal:
			best = max(best, len(labels)-i)
		case ruleWildcard:
			best = max(best, len(labels)-i+1)
		}
	}
	return min(best, len(labels))
}

// DataDigest is the digest of the shipped list: views computed from
// registrable domains (evidence values) are versioned with it.
var DataDigest = sync.OnceValue(func() string { return digest.Of([]byte(listData)) })
