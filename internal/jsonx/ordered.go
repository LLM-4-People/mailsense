package jsonx

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Member is one key/value pair of an Object.
type Member struct {
	Key   string
	Value any
}

// Object is a JSON object that keeps its key order (the legacy taxonomy is
// ordered: questions are chunked and criteria offered in file order).
type Object []Member

// Get returns the value of key.
func (o Object) Get(key string) (any, bool) {
	for _, m := range o {
		if m.Key == key {
			return m.Value, true
		}
	}
	return nil, false
}

// Keys returns the keys in order.
func (o Object) Keys() []string {
	out := make([]string, len(o))
	for i, m := range o {
		out[i] = m.Key
	}
	return out
}

// Set replaces key in place, or appends it (Python dict semantics).
func (o Object) Set(key string, v any) Object {
	for i := range o {
		if o[i].Key == key {
			o[i].Value = v
			return o
		}
	}
	return append(o, Member{Key: key, Value: v})
}

// Delete removes key.
func (o Object) Delete(key string) Object {
	out := o[:0:0]
	for _, m := range o {
		if m.Key != key {
			out = append(out, m)
		}
	}
	return out
}

// MarshalJSON writes the members in order.
func (o Object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(m.Key)
		v, err := json.Marshal(m.Value)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// DecodeOrdered decodes exactly one JSON value; objects become Object (a
// duplicate key is an error), arrays []any, numbers json.Number.
func DecodeOrdered(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := orderedValue(dec)
	if err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if err := atEOF(dec); err != nil {
		return nil, err
	}
	return v, nil
}

func orderedValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		if t == '{' {
			return orderedObject(dec)
		}
		if t == '[' {
			return orderedArray(dec)
		}
		return nil, fmt.Errorf("unexpected %q", t)
	default:
		return t, nil
	}
}

func orderedObject(dec *json.Decoder) (Object, error) {
	obj := Object{}
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := tok.(string)
		if seen[key] {
			return nil, fmt.Errorf("duplicate key %q", key)
		}
		seen[key] = true
		v, err := orderedValue(dec)
		if err != nil {
			return nil, err
		}
		obj = append(obj, Member{Key: key, Value: v})
	}
	_, err := dec.Token() // '}'
	return obj, err
}

func orderedArray(dec *json.Decoder) ([]any, error) {
	out := []any{}
	for dec.More() {
		v, err := orderedValue(dec)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	_, err := dec.Token() // ']'
	return out, err
}
