package config

import (
	"fmt"
	"reflect"
	"time"

	"github.com/LLM-4-People/mailsense/internal/fsx"
)

// Snapshot is an immutable set of effective values.
type Snapshot struct {
	schema  *Schema
	values  map[string]any
	layers  map[string]Layer
	file    map[string]any // the settings.json layer, kept for Save
	learned []Learned      // the install-profile entries (applied or ignored)
}

func buildSnapshot(s *Schema, learned layer, file, env map[string]any) *Snapshot {
	snap := &Snapshot{schema: s, values: map[string]any{}, layers: map[string]Layer{}, file: file, learned: learned.entries}
	for _, st := range s.Settings() {
		snap.values[st.Key], snap.layers[st.Key] = st.defValue, LayerDefault
		if v, ok := learned.values[st.Key]; ok {
			snap.values[st.Key], snap.layers[st.Key] = v, LayerLearned
		}
		if v, ok := file[st.Key]; ok {
			snap.values[st.Key], snap.layers[st.Key] = v, LayerFile
		}
		if v, ok := env[st.Key]; ok {
			snap.values[st.Key], snap.layers[st.Key] = v, LayerEnv
		}
	}
	return snap
}

// layer is the learned layer's values and entries.
type layer struct {
	values  map[string]any
	entries []Learned
}

// Schema returns the schema the snapshot was built from.
func (s *Snapshot) Schema() *Schema { return s.schema }

// Layer reports where the effective value of key came from.
func (s *Snapshot) Layer(key string) Layer { return s.layers[key] }

// Value returns the typed effective value of key.
func (s *Snapshot) Value(key string) (any, bool) {
	v, ok := s.values[key]
	return v, ok
}

// Display returns the value as the UI should show it (secrets hidden).
func (s *Snapshot) Display(key string) any {
	st, ok := s.schema.Lookup(key)
	if !ok {
		return nil
	}
	if st.Secret && !isEmpty(s.values[key]) {
		return SecretMask
	}
	return encodeValue(s.values[key])
}

// changedKeys lists the keys whose effective value differs between snapshots.
func changedKeys(old, cur *Snapshot) []string {
	var keys []string
	for _, st := range cur.schema.Settings() {
		if !reflect.DeepEqual(old.values[st.Key], cur.values[st.Key]) {
			keys = append(keys, st.Key)
		}
	}
	return keys
}

// get returns key's value as T, panicking on programming errors (unknown
// key or wrong accessor) so they surface in the first test that runs them.
func get[T any](s *Snapshot, key string) T {
	v, ok := s.values[key]
	if !ok {
		panic(fmt.Sprintf("config: unknown setting %q", key))
	}
	t, ok := v.(T)
	if !ok {
		panic(fmt.Sprintf("config: setting %q is %T, not %T", key, v, t))
	}
	return t
}

// Bool returns a bool setting.
func (s *Snapshot) Bool(key string) bool { return get[bool](s, key) }

// Int returns an int setting.
func (s *Snapshot) Int(key string) int { return int(get[int64](s, key)) }

// Float returns a float setting.
func (s *Snapshot) Float(key string) float64 { return get[float64](s, key) }

// String returns a string or enum setting.
func (s *Snapshot) String(key string) string { return get[string](s, key) }

// Duration returns a duration setting.
func (s *Snapshot) Duration(key string) time.Duration { return get[time.Duration](s, key) }

// Strings returns a list-of-strings setting (a copy).
func (s *Snapshot) Strings(key string) []string {
	return append([]string(nil), get[[]string](s, key)...)
}

// Ints returns a list-of-ints setting (a copy).
func (s *Snapshot) Ints(key string) []int {
	src := get[[]int64](s, key)
	out := make([]int, len(src))
	for i, v := range src {
		out[i] = int(v)
	}
	return out
}

// encodeValue converts a typed value back to its settings.json form.
func encodeValue(v any) any {
	if d, ok := v.(time.Duration); ok {
		return d.String()
	}
	return v
}

func isEmpty(v any) bool {
	switch x := v.(type) {
	case string:
		return x == ""
	case []string:
		return len(x) == 0
	}
	return v == nil
}

// VersionKey identifies this snapshot together with the current version
// of the data files named by fileSettings (settings holding a path
// relative to the data directory): derived data built from them is
// rebuilt when the key changes.
func (c *Config) VersionKey(fileSettings ...string) string {
	s := c.Snapshot()
	key := fmt.Sprintf("%p", s)
	for _, k := range fileSettings {
		key += "|" + fsx.Stamp(c.Paths().Resolve(s.String(k)))
	}
	return key
}
