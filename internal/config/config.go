package config

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/fsx"
)

// Change is published on TopicChanged after a reload or Set changed values.
type Change struct {
	Keys            []string `json:"keys"`
	RestartRequired []string `json:"restart_required"`
}

// Has reports whether one of the keys changed in this notification. Every
// subscriber asks the same question; spelling the loop each time invited a
// missed trigger.
func (c Change) Has(keys ...string) bool {
	for _, k := range c.Keys {
		for _, want := range keys {
			if k == want {
				return true
			}
		}
	}
	return false
}

// TopicChanged carries every effective configuration change.
var TopicChanged = events.NewTopic[Change]("config.changed")

// Options are the inputs of Load. Environ is os.Environ() in production.
type Options struct {
	Paths   Paths
	Environ []string
	Bus     *events.Bus // optional: change notifications
	Clock   clock.Clock // optional: waits between file-replace retries
}

// Config holds the current snapshot and swaps it atomically.
type Config struct {
	schema  *Schema
	opts    Options
	mu      sync.Mutex // serialises Reload / Set / Reset
	cur     atomic.Pointer[Snapshot]
	started *Snapshot // the values the running process started with
}

// Load builds the effective configuration; any problem in any layer fails
// the load and every problem is reported.
func Load(opts Options) (*Config, error) {
	if !filepath.IsAbs(opts.Paths.DataDir) {
		return nil, fmt.Errorf("config: data directory %q must be absolute (use ResolvePaths)", opts.Paths.DataDir)
	}
	s, err := Embedded()
	if err != nil {
		return nil, err
	}
	c := &Config{schema: s, opts: opts}
	var problems Problems
	file := readFileLayer(s, opts.Paths.SettingsFile(), &problems)
	snap, err := c.build(file, &problems)
	if err != nil {
		return nil, err
	}
	c.cur.Store(snap)
	c.started = snap
	return c, nil
}

// build layers the install profile, file and env over the defaults and
// checks the constraints.
func (c *Config) build(file map[string]any, problems *Problems) (*Snapshot, error) {
	values, entries := readLearnedLayer(c.schema, c.opts.Paths.InstallProfileFile(), problems)
	env := readEnvLayer(c.schema, c.opts.Environ, problems)
	if len(*problems) > 0 {
		return nil, problems.err()
	}
	snap := buildSnapshot(c.schema, layer{values: values, entries: entries}, file, env)
	if cp := c.schema.checkConstraints(snap); len(cp) > 0 {
		return nil, cp.err()
	}
	return snap, nil
}

// Schema returns the settings schema.
func (c *Config) Schema() *Schema { return c.schema }

// Paths returns the data locations.
func (c *Config) Paths() Paths { return c.opts.Paths }

// Snapshot returns the current immutable values.
func (c *Config) Snapshot() *Snapshot { return c.cur.Load() }

// Reload re-reads settings.json, the install profile and the environment
// captured at Load. On error the previous values stay in effect.
func (c *Config) Reload() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var problems Problems
	snap, err := c.build(readFileLayer(c.schema, c.opts.Paths.SettingsFile(), &problems), &problems)
	if err != nil {
		return err
	}
	c.swap(snap)
	return nil
}

// Set validates value (any JSON-encodable Go value; durations as "5s"),
// merges it into the settings.json on disk (keeping edits made outside the
// app), saves atomically and applies it. A settings.json that does not parse
// is never overwritten.
func (c *Config) Set(key string, value any) error {
	v, err := c.parse("set", key, value)
	if err != nil {
		return err
	}
	return c.updateFile(func(file map[string]any) { file[key] = v })
}

// Check validates value for key against the current configuration without
// saving or applying it (for the settings UI).
func (c *Config) Check(key string, value any) error {
	v, err := c.parse("check", key, value)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _, err = c.candidate(func(file map[string]any) { file[key] = v })
	return err
}

// Reset removes key from settings.json so the default (or env) applies.
func (c *Config) Reset(key string) error {
	if _, ok := c.schema.Lookup(key); !ok {
		return (&Problems{{Key: key, Source: "reset", Message: "unknown setting"}}).err()
	}
	return c.updateFile(func(file map[string]any) { delete(file, key) })
}

func (c *Config) parse(source, key string, value any) (any, error) {
	st, ok := c.schema.Lookup(key)
	if !ok {
		return nil, (&Problems{{Key: key, Source: source, Message: "unknown setting"}}).err()
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, (&Problems{{Key: key, Source: source, Message: err.Error()}}).err()
	}
	v, err := st.rule.fromJSON(raw)
	if err != nil {
		return nil, (&Problems{{Key: key, Source: source, Message: err.Error()}}).err()
	}
	return v, nil
}

// candidate re-reads settings.json, applies edit and validates the result.
func (c *Config) candidate(edit func(map[string]any)) (*Snapshot, map[string]any, error) {
	var problems Problems
	file := readFileLayer(c.schema, c.opts.Paths.SettingsFile(), &problems)
	if len(problems) > 0 {
		return nil, nil, fmt.Errorf("config: settings.json has problems; fix or remove it before changing settings: %w", problems.err())
	}
	edit(file)
	snap, err := c.build(file, &problems)
	return snap, file, err
}

func (c *Config) updateFile(edit func(map[string]any)) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	snap, file, err := c.candidate(edit)
	if err != nil {
		return err
	}
	if err := c.save(file); err != nil {
		return err
	}
	c.swap(snap)
	return nil
}

func (c *Config) save(file map[string]any) error {
	out := make(map[string]any, len(file))
	for k, v := range file {
		out[k] = encodeValue(v)
	}
	if err := fsx.WriteJSON(c.opts.Paths.SettingsFile(), out, fsx.FilePerm, c.Snapshot().Replace(c.opts.Clock)); err != nil {
		return fmt.Errorf("config: save settings: %w", err)
	}
	return nil
}

// Replace is the fs.replace_* policy of every atomic write and promotion
// (clk nil = wall clock).
func (s *Snapshot) Replace(clk clock.Clock) fsx.Replace {
	return fsx.Replace{Attempts: s.Int("fs.replace_attempts"), Delay: s.Duration("fs.replace_retry_delay"), Clock: clk}
}

func (c *Config) swap(snap *Snapshot) {
	old := c.cur.Swap(snap)
	keys := changedKeys(old, snap)
	if len(keys) == 0 || c.opts.Bus == nil {
		return
	}
	change := Change{Keys: keys}
	for _, k := range keys {
		if st, _ := c.schema.Lookup(k); st.RestartRequired {
			change.RestartRequired = append(change.RestartRequired, k)
		}
	}
	events.Publish(c.opts.Bus, TopicChanged, change)
}

// RestartPending lists the restart-required settings whose effective value
// differs from the one the process started with.
func (c *Config) RestartPending() []string {
	var out []string
	for _, k := range changedKeys(c.started, c.Snapshot()) {
		if st, _ := c.schema.Lookup(k); st.RestartRequired {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// SecretValues returns the non-empty values of secret settings (for log redaction).
func (c *Config) SecretValues() []string {
	snap := c.Snapshot()
	var out []string
	for _, st := range c.schema.Settings() {
		if !st.Secret {
			continue
		}
		switch v := snap.values[st.Key].(type) {
		case string:
			if v != "" {
				out = append(out, v)
			}
		case []string:
			out = append(out, v...)
		}
	}
	return out
}

// Defaults returns a snapshot of the schema defaults (no file, no env), for
// components that run before Load.
func Defaults() (*Snapshot, error) {
	s, err := Embedded()
	if err != nil {
		return nil, err
	}
	return buildSnapshot(s, layer{}, map[string]any{}, map[string]any{}), nil
}

// Typed accessors on the current snapshot.

// Bool returns a bool setting.
func (c *Config) Bool(key string) bool { return c.Snapshot().Bool(key) }

// Int returns an int setting.
func (c *Config) Int(key string) int { return c.Snapshot().Int(key) }

// Float returns a float setting.
func (c *Config) Float(key string) float64 { return c.Snapshot().Float(key) }

// String returns a string or enum setting.
func (c *Config) String(key string) string { return c.Snapshot().String(key) }

// Duration returns a duration setting.
func (c *Config) Duration(key string) time.Duration { return c.Snapshot().Duration(key) }

// Strings returns a list-of-strings setting.
func (c *Config) Strings(key string) []string { return c.Snapshot().Strings(key) }

// Ints returns a list-of-ints setting.
func (c *Config) Ints(key string) []int { return c.Snapshot().Ints(key) }
