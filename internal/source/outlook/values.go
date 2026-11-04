package outlook

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// Decoders of Object values. A missing property arrives as nil or as a
// PropError; every decoder treats both as "unset".

func asString(v any) string {
	s, _ := v.(string)
	return s
}

func asInt(v any) mail.OptInt {
	switch n := v.(type) {
	case int64:
		return mail.Int(n)
	case bool:
		if n {
			return mail.Int(1)
		}
		return mail.Int(0)
	}
	return mail.OptInt{}
}

func asBool(v any) (bool, bool) {
	switch b := v.(type) {
	case bool:
		return b, true
	case int64:
		return b != 0, true
	}
	return false, false
}

// asHex renders a binary property the way Outlook renders EntryIDs:
// upper-case hex.
func asHex(v any) string {
	b, _ := v.([]byte)
	return strings.ToUpper(hex.EncodeToString(b))
}

// asStrings is a multi-valued string property's values (SAFEARRAYs read
// as []any), empty ones left out.
func asStrings(v any) []string {
	items, _ := v.([]any)
	out := make([]string, 0, len(items))
	for _, it := range items {
		if s := asString(it); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// asList joins a multi-valued string property in an invariant form (the
// object model joins categories with the user's locale list separator).
func asList(v any) string {
	return strings.Join(asStrings(v), ", ")
}

// asTime renders a schema-name date (already UTC) in the stored form.
func asTime(v any, times mail.Times) string {
	d, ok := v.(Date)
	if !ok {
		return ""
	}
	return times.Format(d.UTC())
}

// asDate renders a date-only value (PidLidTaskDueDate/StartDate,
// MS-OXOTASK 2.2.2.2.4–5: "the date is in the user's local time zone", time
// component midnight) as that calendar date at 00:00 in the stored form.
// The value is NOT an instant: it is neither shifted to nor from UTC, and a
// writer that set a time of day cannot move the date.
func asDate(v any, times mail.Times) string {
	d, ok := v.(Date)
	if !ok {
		return ""
	}
	return times.Format(d.UTC().Truncate(24 * time.Hour))
}

// exactLayout is the full-precision form of change stamps and cursors:
// Outlook keeps milliseconds, and two edits in one second must differ.
const exactLayout = "2006-01-02T15:04:05.000Z"

// asExact renders a schema-name date (UTC) to the millisecond, "" when unset.
func asExact(v any) string {
	d, ok := v.(Date)
	if !ok {
		return ""
	}
	return d.UTC().Format(exactLayout)
}

// unset reports whether a value means "property not set".
func unset(v any) bool {
	if v == nil {
		return true
	}
	_, isErr := v.(PropError)
	return isErr
}

// valuesOf checks that a GetProperties result has one value per name.
func valuesOf(v any, names []string) ([]any, error) {
	vals, ok := v.([]any)
	if !ok || len(vals) != len(names) {
		return nil, fmt.Errorf("outlook: GetProperties returned %T with %d values for %d names", v, len(vals), len(names))
	}
	return vals, nil
}
