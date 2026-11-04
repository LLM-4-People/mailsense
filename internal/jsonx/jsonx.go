// Package jsonx holds the JSON helpers shared by every trust boundary
// (settings files, environment values, legacy records).
package jsonx

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// DecodeOne decodes exactly one JSON value; trailing data is an error.
// Numbers are json.Number so integers and floats can be told apart.
func DecodeOne(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if err := atEOF(dec); err != nil {
		return nil, err
	}
	return v, nil
}

// atEOF fails unless the decoder has nothing but whitespace left. (More
// alone accepted a stray closing bracket after the value.)
func atEOF(dec *json.Decoder) error {
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("invalid JSON: trailing data")
	}
	return nil
}

// Kind names the JSON type of a decoded value for error messages.
func Kind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case json.Number, float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "list"
	case map[string]any, Object:
		return "object"
	}
	return fmt.Sprintf("%T", v)
}
