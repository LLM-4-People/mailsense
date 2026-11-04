// Package config is the single owner of every tunable. The embedded
// schema.json declares each setting (type, default, bounds, description) and
// the cross-field constraints; values are layered schema defaults →
// settings.json → MAILSENSE_* env, and validated strictly with every problem
// reported at once.
//
// config does not log (logx depends on it); it returns errors and publishes
// change events instead.
package config

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"sync"

	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

//go:embed schema.json
var schemaJSON []byte

// SecretMask replaces secret values wherever they would be shown: UI
// displays, validation errors and log redaction.
const SecretMask = "[REDACTED]"

// Type is the value type of a setting.
type Type string

// Setting types.
const (
	TypeBool     Type = "bool"
	TypeInt      Type = "int"
	TypeFloat    Type = "float"
	TypeString   Type = "string"
	TypeDuration Type = "duration"
	TypeEnum     Type = "enum"
	TypeList     Type = "list"
)

// Setting is one schema entry. Default/Min/Max hold the raw JSON form.
type Setting struct {
	Key             string          `json:"key"`
	Type            Type            `json:"type"`
	ItemType        Type            `json:"item_type,omitempty"`
	Default         json.RawMessage `json:"default"`
	Min             json.RawMessage `json:"min,omitempty"`
	Max             json.RawMessage `json:"max,omitempty"`
	Enum            []string        `json:"enum,omitempty"`
	Pattern         string          `json:"pattern,omitempty"`
	Format          string          `json:"format,omitempty"`
	Description     string          `json:"description"`
	Group           string          `json:"group"`
	NotIn           []int64         `json:"not_in,omitempty"` // forbidden int values (int settings and int list items)
	Secret          bool            `json:"secret,omitempty"`
	RestartRequired bool            `json:"restart_required,omitempty"`
	// Learnable settings take a per-install learned value (the install
	// profile, Config.Learn) between the default and the user's override.
	Learnable bool `json:"learnable,omitempty"`
	// DataFile settings name a data file (a path in the data directory)
	// the app reads at run time: WatchDataFiles reports a change of its
	// content as a change of the setting.
	DataFile bool `json:"data_file,omitempty"`

	rule     rule
	defValue any
}

// EnvName is the environment variable that overrides this setting.
func (s *Setting) EnvName() string { return EnvName(s.Key) }

// Schema is the parsed, self-validated settings schema.
type Schema struct {
	settings     []*Setting
	byKey        map[string]*Setting
	constraints  []Constraint
	vocabularies map[string][]string
}

type schemaDoc struct {
	Settings    []*Setting   `json:"settings"`
	Constraints []Constraint `json:"constraints"`
	// Vocabularies are named value lists several settings share (one
	// place each), referenced by the formats one_of:<name> and
	// keyed:<name>.
	Vocabularies map[string][]string `json:"vocabularies"`
}

// Vocabulary returns a named value list of the schema (nil when unknown).
func (s *Schema) Vocabulary(name string) []string { return slices.Clone(s.vocabularies[name]) }

var keyRE = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

var loadEmbedded = sync.OnceValues(func() (*Schema, error) { return ParseSchema(schemaJSON) })

// Embedded returns the schema compiled into the binary.
func Embedded() (*Schema, error) { return loadEmbedded() }

// ParseSchema parses and validates a schema document.
func ParseSchema(data []byte) (*Schema, error) {
	var doc schemaDoc
	if err := jsonx.DecodeStrict(data, &doc); err != nil {
		return nil, fmt.Errorf("config schema: %w", err)
	}
	s := &Schema{byKey: map[string]*Setting{}, vocabularies: doc.Vocabularies}
	var problems Problems
	s.collect(doc.Settings, &problems)
	// Rules compile after every key is known ("pair:<key>" refers to another
	// setting); defaults are checked after every rule exists.
	s.checkDefaults(s.compileRules(&problems), &problems)
	s.constraints = s.compileConstraints(doc.Constraints, &problems)
	if len(problems) > 0 {
		return nil, problems.err()
	}
	sort.Slice(s.settings, func(i, j int) bool { return s.settings[i].Key < s.settings[j].Key })
	return s, nil
}

// collect registers the settings, rejecting bad keys, duplicates and
// environment-name collisions.
func (s *Schema) collect(settings []*Setting, problems *Problems) {
	envs := map[string]string{}
	for _, st := range settings {
		if !keyRE.MatchString(st.Key) {
			problems.add(st.Key, "schema", "key must be dotted lower_snake segments")
			continue
		}
		if _, dup := s.byKey[st.Key]; dup {
			problems.add(st.Key, "schema", "duplicate key")
			continue
		}
		env := st.EnvName()
		if other, dup := envs[env]; dup {
			problems.add(st.Key, "schema", "environment name "+env+" collides with "+other)
		}
		if isReservedEnv(env) {
			problems.add(st.Key, "schema", "environment name "+env+" is reserved")
		}
		if st.Learnable && (st.Secret || st.RestartRequired) {
			problems.add(st.Key, "schema", "a learnable setting cannot be secret or restart-required")
		}
		if st.DataFile && (st.Type != TypeString || st.RestartRequired) {
			problems.add(st.Key, "schema", "a data file setting is a string read at run time (not restart-required)")
		}
		envs[env] = st.Key
		s.byKey[st.Key] = st
		s.settings = append(s.settings, st)
	}
}

func (s *Schema) compileRules(problems *Problems) []*Setting {
	var compiled []*Setting
	for _, st := range s.settings {
		r, err := compileRule(st, s)
		if err != nil {
			problems.add(st.Key, "schema", err.Error())
			continue
		}
		st.rule = r
		compiled = append(compiled, st)
	}
	return compiled
}

func (s *Schema) checkDefaults(compiled []*Setting, problems *Problems) {
	for _, st := range compiled {
		v, err := st.rule.fromJSON(st.Default)
		if err != nil {
			problems.add(st.Key, "schema", "default: "+err.Error())
			continue
		}
		st.defValue = v
	}
}

// Settings returns every setting, sorted by key.
func (s *Schema) Settings() []*Setting { return s.settings }

// IntRange returns the bounds of an int setting (ok false when the
// setting is not an int or unbounded on either side).
func (s *Setting) IntRange() (lo, hi int64, ok bool) {
	lo, okLo := s.rule.min.(int64)
	hi, okHi := s.rule.max.(int64)
	if s.rule.item != nil {
		lo, okLo = s.rule.item.min.(int64)
		hi, okHi = s.rule.item.max.(int64)
	}
	return lo, hi, okLo && okHi
}

// Lookup returns the setting for key.
func (s *Schema) Lookup(key string) (*Setting, bool) {
	st, ok := s.byKey[key]
	return st, ok
}

// Constraints returns the cross-field constraints.
func (s *Schema) Constraints() []Constraint { return s.constraints }
