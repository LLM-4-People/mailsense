package jsonx

import (
	"encoding/json"

	"github.com/LLM-4-People/mailsense/internal/mathx"
	"strconv"
)

// Finite is the one trust-boundary number rule (PORTING §7): only a JSON
// number that is finite counts. A boolean is never a number, and neither is
// a numeric string (stricter than the legacy float() coercion: a payload
// that quotes a probability breaks its contract).
func Finite(v any) (float64, bool) {
	var f float64
	switch x := v.(type) {
	case json.Number:
		n, err := x.Float64()
		if err != nil {
			return 0, false
		}
		f = n
	case float64:
		f = x
	default:
		return 0, false
	}
	if !mathx.IsFinite(f) {
		return 0, false
	}
	return f, true
}

// Int is the one trust-boundary integer rule: a JSON number written as an
// integer literal (no fraction, no exponent: 1.0 and 1e3 are not integers).
func Int(v any) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := strconv.ParseInt(n.String(), 10, 64)
	return i, err == nil
}
