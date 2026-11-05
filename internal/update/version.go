// Package update is the release-detection domain of the desktop app: it
// compares the running build against the latest published GitHub release,
// keeps the What's New marker and persists the one update-state file. It
// owns no download and no Windows syscall; installing a detected release is
// a later slice that reads the same state.
package update

import (
	"cmp"
	"fmt"
	"strconv"
	"strings"
)

// Version is one parsed semantic version (SemVer 2.0.0 precedence): the v
// prefix is optional, a prerelease sorts before its release, build metadata
// is ignored for precedence. The zero value is invalid: only ParseVersion
// marks a version comparable, so "dev" and every malformed string can never
// be treated as 0.0.0.
type Version struct {
	valid        bool
	major, minor uint64
	patch        uint64
	pre          []string
	build        string
}

// ParseVersion parses "v0.1.0", "0.1.0" and "0.3.0-dev+20261105". ok is
// false for "dev", "" and every malformed string. The parser is strict on
// purpose: a version the app cannot order is left uncomparable instead of
// guessed at.
func ParseVersion(raw string) (Version, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return Version{}, false
	}
	if s[0] == 'v' {
		s = s[1:]
		if s == "" || s[0] < '0' || s[0] > '9' {
			return Version{}, false
		}
	}
	build := ""
	if i := strings.IndexByte(s, '+'); i >= 0 {
		build = s[i+1:]
		if !validBuild(build) {
			return Version{}, false
		}
		s = s[:i]
	}
	var pre []string
	if i := strings.IndexByte(s, '-'); i >= 0 {
		rawPre := s[i+1:]
		if rawPre == "" {
			return Version{}, false
		}
		s = s[:i]
		ids := strings.Split(rawPre, ".")
		pre = make([]string, 0, len(ids))
		for _, id := range ids {
			if !validPrereleaseID(id) {
				return Version{}, false
			}
			pre = append(pre, id)
		}
	}
	core := strings.Split(s, ".")
	if len(core) != 3 {
		return Version{}, false
	}
	var nums [3]uint64
	for i, part := range core {
		n, ok := parseNumericID(part)
		if !ok {
			return Version{}, false
		}
		nums[i] = n
	}
	return Version{valid: true, major: nums[0], minor: nums[1], patch: nums[2], pre: pre, build: build}, true
}

// String returns the canonical version (no v prefix, build metadata kept).
func (v Version) String() string {
	if !v.valid {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d.%d.%d", v.major, v.minor, v.patch)
	if len(v.pre) > 0 {
		b.WriteByte('-')
		b.WriteString(strings.Join(v.pre, "."))
	}
	if v.build != "" {
		b.WriteByte('+')
		b.WriteString(v.build)
	}
	return b.String()
}

// Compare orders two parsed versions (-1, 0, 1). Two versions that differ
// only in build metadata are equal. An invalid Version is not comparable and
// Compare reports 0, so a caller that forgot to Parse can never mistake a
// "dev" build for an older or newer release.
func Compare(a, b Version) int {
	if !a.valid || !b.valid {
		return 0
	}
	if c := cmp.Compare(a.major, b.major); c != 0 {
		return c
	}
	if c := cmp.Compare(a.minor, b.minor); c != 0 {
		return c
	}
	if c := cmp.Compare(a.patch, b.patch); c != 0 {
		return c
	}
	return comparePrerelease(a.pre, b.pre)
}

// VersionsBehind counts the parseable tags strictly between current and
// latest: versions newer than current and older than latest. Tags that equal
// either end, malformed tags and precedence duplicates are not counted; a
// current that is not older than latest counts 0. Counting over the release
// tags the caller actually knows never invents a distance the source did not
// report.
func VersionsBehind(current, latest Version, tags []string) int {
	if !current.valid || !latest.valid || Compare(current, latest) >= 0 {
		return 0
	}
	seen := map[string]bool{}
	for _, tag := range tags {
		v, ok := ParseVersion(tag)
		if !ok {
			continue
		}
		if Compare(current, v) >= 0 || Compare(v, latest) >= 0 {
			continue
		}
		seen[v.precedenceKey()] = true
	}
	return len(seen)
}

// precedenceKey identifies versions with equal precedence (build metadata
// ignored) for counting.
func (v Version) precedenceKey() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d.%d.%d", v.major, v.minor, v.patch)
	if len(v.pre) > 0 {
		b.WriteByte('-')
		b.WriteString(strings.Join(v.pre, "."))
	}
	return b.String()
}

func comparePrerelease(a, b []string) int {
	switch {
	case len(a) == 0 && len(b) == 0:
		return 0
	case len(a) == 0:
		return 1 // a release sorts after its prereleases
	case len(b) == 0:
		return -1
	}
	for i := 0; i < len(a) && i < len(b); i++ {
		if c := comparePrereleaseID(a[i], b[i]); c != 0 {
			return c
		}
	}
	return cmp.Compare(len(a), len(b))
}

func comparePrereleaseID(a, b string) int {
	an, aNumeric := numericID(a)
	bn, bNumeric := numericID(b)
	switch {
	case aNumeric && bNumeric:
		return cmp.Compare(an, bn)
	case aNumeric:
		return -1 // numeric identifiers sort before alphanumeric ones
	case bNumeric:
		return 1
	default:
		return strings.Compare(a, b)
	}
}

// parseNumericID parses one dot-separated core identifier: digits only, no
// leading zero unless the value is exactly "0".
func parseNumericID(s string) (uint64, bool) {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return n, err == nil
}

// validPrereleaseID accepts one prerelease identifier; a numeric one may not
// carry a leading zero.
func validPrereleaseID(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !validVersionByte(s[i]) {
			return false
		}
	}
	if _, numeric := numericID(s); numeric && len(s) > 1 && s[0] == '0' {
		return false
	}
	return true
}

// validBuild accepts the build-metadata tail: dot-separated non-empty
// identifiers of [0-9A-Za-z-]. Leading zeros are allowed here.
func validBuild(s string) bool {
	if s == "" {
		return false
	}
	ids := strings.Split(s, ".")
	for _, id := range ids {
		if id == "" {
			return false
		}
		for i := 0; i < len(id); i++ {
			if !validVersionByte(id[i]) {
				return false
			}
		}
	}
	return true
}

func validVersionByte(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-'
}

// numericID reports whether s is a non-empty all-digit identifier and
// returns its value.
func numericID(s string) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return n, err == nil
}
