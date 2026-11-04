package config

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/LLM-4-People/mailsense/internal/fsx"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

// The install profile is the per-install layer of LEARNED values, between
// the shipped defaults (schema.json and data files) and the user's
// overrides (settings.json, env): what this install measured about its own
// mailboxes and machine (shared mail domains, reply prefixes…). Only
// settings marked "learnable" in the schema take learned values; each
// carries its sample size and provenance, is persisted in
// install-profile.json in the data directory and is queryable
// (Config.Learned). A user override always wins over a learned value.

const installProfileFile = "install-profile.json"

// Provenance says where a learned value came from.
type Provenance struct {
	Learner string `json:"learner"` // the module that learned it, e.g. "installprofile.shared_domains"
	// Basis is what it was measured on, as counts by name (the UI words
	// them): {"senders": 1200, "threads": 4100}.
	Basis     map[string]int `json:"basis"`
	LearnedAt string         `json:"learned_at"` // UTC RFC 3339
}

// Learned is one learned value as stored and as queried.
type Learned struct {
	Key        string     `json:"key"`
	Value      any        `json:"value"`
	Sample     int        `json:"sample"` // observations the value rests on
	Provenance Provenance `json:"provenance"`
	// Rejected are the values (list settings: items) the user rejected
	// (Config.Reject): never applied, and never learned again.
	Rejected []any `json:"rejected,omitempty"`
	// Ignored explains why a stored entry does not apply (a setting that
	// is unknown or no longer learnable after an upgrade, an invalid
	// value, a rejected value); such entries are shown, never applied.
	Ignored string `json:"ignored,omitempty"`
}

type installProfileDoc struct {
	Entries map[string]json.RawMessage `json:"entries"`
}

type storedEntry struct {
	Value      json.RawMessage   `json:"value"`
	Sample     int               `json:"sample"`
	Provenance Provenance        `json:"provenance"`
	Rejected   []json.RawMessage `json:"rejected,omitempty"`
}

// InstallProfileFile is the learned layer's file.
func (p Paths) InstallProfileFile() string { return p.Resolve(installProfileFile) }

// readLearnedLayer reads install-profile.json: the values that apply and
// every entry with its state. A missing file is an empty layer; a file
// that does not parse is a problem (it is app-written).
func readLearnedLayer(s *Schema, path string, problems *Problems) (map[string]any, []Learned) {
	data, ok, err := fsx.ReadOptional(path)
	if err != nil {
		problems.add("*", path, err.Error())
		return map[string]any{}, nil
	}
	if !ok {
		return map[string]any{}, nil
	}
	var doc installProfileDoc
	if err := jsonx.DecodeStrict(data, &doc); err != nil {
		problems.add("*", path, err.Error())
		return map[string]any{}, nil
	}
	values := map[string]any{}
	var entries []Learned
	for _, key := range slices.Sorted(maps.Keys(doc.Entries)) {
		var e storedEntry
		l := Learned{Key: key}
		if err := jsonx.DecodeStrict(doc.Entries[key], &e); err != nil {
			l.Ignored = err.Error()
			entries = append(entries, l)
			continue
		}
		l.Sample, l.Provenance, l.Rejected = e.Sample, e.Provenance, decodeAll(e.Rejected)
		st, known := s.Lookup(key)
		switch {
		case !known:
			l.Ignored = "unknown setting"
		case !st.Learnable:
			l.Ignored = "setting is not learnable"
		default:
			v, err := st.rule.fromJSON(e.Value)
			if err != nil {
				l.Ignored = err.Error()
				break
			}
			v, kept := withoutRejected(v, e.Rejected)
			if !kept {
				l.Ignored = "the user rejected this value"
				break
			}
			values[key], l.Value = v, encodeValue(v)
		}
		entries = append(entries, l)
	}
	return values, entries
}

// Learned returns every install-profile entry (applied or ignored), by key.
func (c *Config) Learned() []Learned {
	return slices.Clone(c.Snapshot().learned)
}

// Learn validates and stores a learned value of a learnable setting with
// its sample size and provenance, then applies it (a user override in
// settings.json or env still wins). A learned value never changes
// settings.json.
func (c *Config) Learn(key string, value any, sample int, prov Provenance) error {
	st, ok := c.schema.Lookup(key)
	if !ok || !st.Learnable {
		return (&Problems{{Key: key, Source: "learn", Message: "not a learnable setting"}}).err()
	}
	if sample < 0 || prov.Learner == "" || prov.LearnedAt == "" {
		return (&Problems{{Key: key, Source: "learn", Message: "a learned value needs a sample size, a learner and a time"}}).err()
	}
	v, err := c.parse("learn", key, value)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(encodeValue(v))
	if err != nil {
		return err
	}
	return c.editProfile(key, func(e *storedEntry) error {
		e.Value, e.Sample, e.Provenance = raw, sample, prov // the user's rejections stay
		return nil
	})
}

// editProfile changes one install-profile entry (a missing one starts
// empty), saves the file and applies it.
func (c *Config) editProfile(key string, edit func(*storedEntry) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	doc, err := c.readProfileDoc()
	if err != nil {
		return err
	}
	var e storedEntry
	if raw, ok := doc.Entries[key]; ok {
		if err := jsonx.DecodeStrict(raw, &e); err != nil {
			return fmt.Errorf("config: install profile entry %s: %w", key, err)
		}
	}
	if err := edit(&e); err != nil {
		return err
	}
	doc.Entries[key], _ = json.Marshal(e)
	if err := fsx.WriteJSON(c.opts.Paths.InstallProfileFile(), doc, fsx.FilePerm, c.Snapshot().Replace(c.opts.Clock)); err != nil {
		return fmt.Errorf("config: save install profile: %w", err)
	}
	var problems Problems
	snap, err := c.build(readFileLayer(c.schema, c.opts.Paths.SettingsFile(), &problems), &problems)
	if err != nil {
		return err
	}
	c.swap(snap)
	return nil
}

// readProfileDoc reads the stored document to edit it (a file that does
// not parse is never overwritten).
func (c *Config) readProfileDoc() (installProfileDoc, error) {
	doc := installProfileDoc{Entries: map[string]json.RawMessage{}}
	data, ok, err := fsx.ReadOptional(c.opts.Paths.InstallProfileFile())
	if err != nil {
		return doc, err
	}
	if !ok {
		return doc, nil
	}
	if err := jsonx.DecodeStrict(data, &doc); err != nil {
		return doc, fmt.Errorf("config: install-profile.json does not parse; fix or remove it: %w", err)
	}
	if doc.Entries == nil {
		doc.Entries = map[string]json.RawMessage{}
	}
	return doc, nil
}
