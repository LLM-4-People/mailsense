package config

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
)

// Reject is the user's action against a learned value: for a list
// setting, one item of it (a domain wrongly learned as shared); for any
// other, the learned value itself. The rejection is stored with the entry
// (install-profile.json), applies at once, and survives every later Learn,
// so the learner never brings the value back.
func (c *Config) Reject(key string, value any) error {
	raw, err := c.rejectable(key, value)
	if err != nil {
		return err
	}
	return c.editProfile(key, func(e *storedEntry) error {
		if e.Value == nil {
			return (&Problems{{Key: key, Source: "reject", Message: "nothing was learned for this setting"}}).err()
		}
		if !slices.ContainsFunc(e.Rejected, func(r json.RawMessage) bool { return bytes.Equal(r, raw) }) {
			e.Rejected = append(e.Rejected, raw)
		}
		return nil
	})
}

// Unreject withdraws a rejection (the learned value applies again).
func (c *Config) Unreject(key string, value any) error {
	raw, err := c.rejectable(key, value)
	if err != nil {
		return err
	}
	return c.editProfile(key, func(e *storedEntry) error {
		e.Rejected = slices.DeleteFunc(e.Rejected, func(r json.RawMessage) bool { return bytes.Equal(r, raw) })
		return nil
	})
}

// rejectable validates a rejected value against the setting (an item of a
// list setting, a whole value otherwise) and returns its canonical JSON.
func (c *Config) rejectable(key string, value any) (json.RawMessage, error) {
	st, ok := c.schema.Lookup(key)
	if !ok || !st.Learnable {
		return nil, (&Problems{{Key: key, Source: "reject", Message: "not a learnable setting"}}).err()
	}
	probe := value
	if st.Type == TypeList {
		probe = []any{value}
	}
	v, err := c.parse("reject", key, probe)
	if err != nil {
		return nil, err
	}
	if st.Type == TypeList {
		v = reflect.ValueOf(v).Index(0).Interface()
	}
	return json.Marshal(encodeValue(v))
}

// withoutRejected applies the rejections to a learned value: list items
// are removed; a rejected whole value is not kept (false).
func withoutRejected(v any, rejected []json.RawMessage) (any, bool) {
	if len(rejected) == 0 {
		return v, true
	}
	is := func(x any) bool {
		raw, _ := json.Marshal(encodeValue(x))
		return slices.ContainsFunc(rejected, func(r json.RawMessage) bool { return bytes.Equal(r, raw) })
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice {
		return v, !is(v)
	}
	out := reflect.MakeSlice(rv.Type(), 0, rv.Len())
	for i := range rv.Len() {
		if !is(rv.Index(i).Interface()) {
			out = reflect.Append(out, rv.Index(i))
		}
	}
	return out.Interface(), true
}

// decodeAll is the rejected values as plain JSON values (for Learned).
func decodeAll(raws []json.RawMessage) []any {
	var out []any
	for _, r := range raws {
		var v any
		if json.Unmarshal(r, &v) == nil {
			out = append(out, v)
		}
	}
	return out
}
