package jsonx

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// DecodeStrict decodes exactly one JSON document into v: unknown fields
// and trailing data are errors (every typed data file goes through it).
func DecodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return atEOF(dec)
}
