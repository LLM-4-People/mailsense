package config

import (
	"bytes"
	"maps"
	"slices"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/fsx"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

// Layer names where an effective value came from.
type Layer string

// Layers, lowest precedence first.
const (
	LayerDefault Layer = "default"
	LayerLearned Layer = "learned" // the install profile (learned.go)
	LayerFile    Layer = "file"
	LayerEnv     Layer = "env"
)

// readFileLayer reads settings.json: a flat JSON object of dotted keys.
// A missing file is an empty layer; null means "use the default".
func readFileLayer(s *Schema, path string, problems *Problems) map[string]any {
	data, ok, err := fsx.ReadOptional(path)
	if err != nil {
		problems.add("*", path, err.Error())
		return map[string]any{}
	}
	if !ok {
		return map[string]any{}
	}
	return parseFileLayer(s, data, path, problems)
}

func parseFileLayer(s *Schema, data []byte, source string, problems *Problems) map[string]any {
	out := map[string]any{}
	if len(bytes.TrimSpace(data)) == 0 {
		return out
	}
	doc, err := jsonx.DecodeOne(data)
	if err != nil {
		problems.add("*", source, err.Error())
		return out
	}
	obj, ok := doc.(map[string]any)
	if !ok {
		problems.add("*", source, "must be a JSON object of \"dotted.key\": value pairs, got "+jsonx.Kind(doc))
		return out
	}
	for _, key := range slices.Sorted(maps.Keys(obj)) {
		raw := obj[key]
		st, known := s.Lookup(key)
		if !known {
			problems.add(key, source, "unknown setting")
			continue
		}
		if raw == nil {
			continue
		}
		v, err := st.rule.fromAny(raw)
		if err != nil {
			problems.add(key, source, err.Error())
			continue
		}
		out[key] = v
	}
	return out
}

// readEnvLayer reads MAILSENSE_* overrides; unknown names are problems
// except the reserved data-dir and secret variables.
func readEnvLayer(s *Schema, environ []string, problems *Problems) map[string]any {
	byEnv := map[string]*Setting{}
	for _, st := range s.Settings() {
		byEnv[st.EnvName()] = st
	}
	out := map[string]any{}
	env := ParseEnviron(environ)
	for _, name := range slices.Sorted(maps.Keys(env)) {
		value := env[name]
		if !strings.HasPrefix(name, EnvPrefix) || isReservedEnv(name) {
			continue
		}
		st, known := byEnv[name]
		if !known {
			problems.add(name, "env", "unknown setting environment variable")
			continue
		}
		v, err := st.rule.fromEnv(value)
		if err != nil {
			problems.add(st.Key, "env "+name, err.Error())
			continue
		}
		out[st.Key] = v
	}
	return out
}
