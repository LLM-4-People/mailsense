package logx

import (
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// secretSet is the registry of secret values to redact; it outlives
// pipeline swaps so secrets registered before Setup stay protected. Readers
// get an immutable list (copy-on-write), so registration never races with
// redaction.
type secretSet struct {
	mu     sync.Mutex // serialises writers
	sorted atomic.Pointer[[]string]
}

var secrets = &secretSet{}

func (s *secretSet) add(v string) {
	if v == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.list()
	if slices.Contains(cur, v) {
		return
	}
	next := append(slices.Clone(cur), v)
	// Longest first, so a secret containing another is replaced whole.
	sort.Slice(next, func(i, j int) bool { return len(next[i]) > len(next[j]) })
	s.sorted.Store(&next)
}

func (s *secretSet) list() []string {
	if p := s.sorted.Load(); p != nil {
		return *p
	}
	return nil
}

// builtinRedactPatterns is the one owner of the patterns that are always
// applied: bearer/basic credentials, sk- keys, key=value or JSON key/value
// pairs whose key names a credential, and email addresses. The
// log.redact.patterns setting adds expressions; it never replaces these.
var builtinRedactPatterns = []string{
	`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`,
	`\bsk-[A-Za-z0-9_-]{16,}`,
	`(?i)[a-z0-9_-]*(token|secret|password|passwd|api[_-]?key|authorization)[a-z0-9_-]*["']?\s*[=:]\s*("[^"]*"|'[^']*'|[^\s"',;}&]*[^\s"',;}&0-9][^\s"',;}&]*)`,
	`(?i)[\p{L}0-9][\p{L}0-9._%+\-]*@[\p{L}0-9](?:[\p{L}0-9\-]*[\p{L}0-9])?(?:\.[\p{L}0-9](?:[\p{L}0-9\-]*[\p{L}0-9])?)*`,
}

// Redact applies the pipeline's policy to a value that leaves through a
// non-log surface: a provider status, a job record or an API error. Those
// surfaces publish the same text the log already masks, so a provider body
// cannot echo a key, an address or a prompt fragment into the UI.
func Redact(s string) string {
	if s == "" {
		return s
	}
	c := active.Load()
	if c == nil || c.redactor == nil {
		return s
	}
	return c.redactor.redact(s)
}

// redactor replaces registered secrets and token-shaped strings.
type redactor struct {
	patterns    []*regexp.Regexp
	replacement string
	minSecret   int
}

func newRedactor(patterns []string, replacement string, minSecret int) (*redactor, error) {
	r := &redactor{replacement: replacement, minSecret: minSecret}
	// Built-ins first, then the user's additions: the setting is additive.
	all := append(append([]string(nil), builtinRedactPatterns...), patterns...)
	seen := make(map[string]bool, len(all))
	for _, p := range all {
		if seen[p] {
			continue
		}
		seen[p] = true
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, err
		}
		r.patterns = append(r.patterns, re)
	}
	return r, nil
}

func (r *redactor) redact(s string) string {
	if s == "" {
		return s
	}
	for _, v := range secrets.list() {
		if len(v) >= r.minSecret {
			s = strings.ReplaceAll(s, v, r.replacement)
		}
	}
	for _, re := range r.patterns {
		s = re.ReplaceAllLiteralString(s, r.replacement)
	}
	return s
}
