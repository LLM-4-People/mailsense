package evidence

import (
	"regexp"
	"slices"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/digest"
)

// opaqueChars is the length of an opaque categorical value (a hash).
const opaqueChars = 8

// versionRun is a run of digits, dotted or dashed (3.2.1, 16.0-1): one version.
var versionRun = regexp.MustCompile(`\p{Nd}+(?:[._-]\p{Nd}+)*`)

// opaque is the opaque categorical form of a free value (a mailer, a
// folder path, a category): lower case, every version number (digits,
// dotted or dashed) as one "#" (versions and builds never split a
// product), white space collapsed,
// hashed. It is compared and counted, never shown.
func opaque(s string) string {
	s = strings.Join(strings.Fields(versionRun.ReplaceAllString(strings.ToLower(s), "#")), " ")
	if s == "" {
		return ""
	}
	return digest.Hex([]byte(s), opaqueChars)
}

// inVocabulary maps a value outside the vocabulary to "other".
func inVocabulary(v string, values []string) string {
	if slices.Contains(values, v) {
		return v
	}
	return "other"
}

// firstToken is the first token of a header value, lower case (before
// a ";", a comment or white space).
func firstToken(v string) string {
	f := strings.FieldsFunc(v, func(c rune) bool { return c == ';' || c == '(' || c == ' ' || c == '\t' || c == ',' })
	if len(f) == 0 {
		return ""
	}
	return strings.ToLower(f[0])
}

// headerEnum is an enum read from the first value of a header: "none"
// when absent (partial when the block was cut), else read(value).
func headerEnum(in *Input, name string, read func(string) string) Value {
	vals, missing, ok := in.Header(name)
	if !ok {
		return missing
	}
	if len(vals) == 0 {
		return Str("none").Cut(in.HeadersCut())
	}
	return Str(read(vals[0]))
}
