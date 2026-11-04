package engine_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/engine"
)

func recordOf(t *testing.T, id, body string) engine.Provider {
	t.Helper()
	var p engine.Provider
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("%s: %v", id, err)
	}
	p.ID = id
	return p
}

func validRecords(t *testing.T) map[engine.Kind]engine.Provider {
	t.Helper()
	return map[engine.Kind]engine.Provider{
		engine.KindDecider:   recordOf(t, "dec", deciderJSON),
		engine.KindGenerator: recordOf(t, "gen", generatorJSON),
		engine.KindEmbedder:  recordOf(t, "emb", embedderJSON),
	}
}

func schemaByJSON(t *testing.T, key string) engine.ProviderField {
	t.Helper()
	for _, field := range engine.ProviderFields() {
		if field.JSON == key {
			return field
		}
	}
	t.Fatalf("the schema has no field %q", key)
	return engine.ProviderField{}
}

func inBounds(b *engine.ProviderBounds, v float64) bool {
	if b == nil {
		return true
	}
	if b.Min != nil {
		if b.MinExclusive && v <= *b.Min || !b.MinExclusive && v < *b.Min {
			return false
		}
	}
	if b.Max != nil {
		if b.MaxExclusive && v >= *b.Max || !b.MaxExclusive && v > *b.Max {
			return false
		}
	}
	return true
}

// schemaNumeric reads the numeric value of one record field by its Go name:
// the schema describes the same field the validator reads.
func schemaNumeric(t *testing.T, p engine.Provider, name string) (float64, bool) {
	t.Helper()
	v := reflect.ValueOf(p).FieldByName(name)
	switch v.Kind() {
	case reflect.Int:
		return float64(v.Int()), true
	case reflect.Float64:
		return v.Float(), true
	case reflect.Pointer:
		if v.IsNil() {
			return 0, false
		}
		return v.Elem().Float(), true
	}
	t.Fatalf("%s is not numeric: %v", name, v.Kind())
	return 0, false
}

// The schema covers every field of the record and nothing else. A field added
// to engine.Provider without a rule fails here instead of shipping
// undocumented to the console.
func TestProviderSchemaCoversEveryRecordField(t *testing.T) {
	fields := engine.ProviderFields()
	typ := reflect.TypeFor[engine.Provider]()
	if len(fields) != typ.NumField() {
		t.Fatalf("the schema has %d fields, the record has %d", len(fields), typ.NumField())
	}
	allKinds := []engine.Kind{engine.KindDecider, engine.KindGenerator, engine.KindEmbedder}
	for i := range typ.NumField() {
		structField := typ.Field(i)
		want := strings.Split(structField.Tag.Get("json"), ",")[0]
		if want == "" {
			t.Fatalf("record field %s has no JSON tag", structField.Name)
		}
		field := fields[i]
		if field.Name != structField.Name {
			t.Fatalf("schema field %d is %s, want %s (declaration order)", i, field.Name, structField.Name)
		}
		if field.JSON != want {
			t.Fatalf("%s: json %q, want %q", field.Name, field.JSON, want)
		}
		if field.Type == "" {
			t.Fatalf("%s: no type in the schema", field.Name)
		}
		if len(field.Kinds) == 0 {
			t.Fatalf("%s: no kinds in the schema", field.Name)
		}
		for _, kind := range field.Kinds {
			if !slices.Contains(allKinds, kind) {
				t.Fatalf("%s: unknown kind %q", field.Name, kind)
			}
		}
	}
}

// Every valid record satisfies every declared bound and enum, and every
// invalid sample the validator rejects falls outside the schema's rule.
func TestProviderSchemaMatchesTheValidator(t *testing.T) {
	cfg := configtest.New(t, nil)
	records := validRecords(t)
	fields := engine.ProviderFields()
	for kind, p := range records {
		if _, err := engine.BuildCatalog(cfg, []engine.Provider{p}); err != nil {
			t.Fatalf("the %s fixture must be valid: %v", kind, err)
		}
		for _, field := range fields {
			if !slices.Contains(field.Kinds, kind) {
				continue
			}
			if field.Bounds != nil {
				if v, ok := schemaNumeric(t, p, field.Name); ok && !inBounds(field.Bounds, v) {
					t.Errorf("%s/%s: valid value %v is outside the declared bounds %+v", kind, field.JSON, v, *field.Bounds)
				}
			}
			if len(field.Enum) > 0 {
				v := reflect.ValueOf(p).FieldByName(field.Name).String()
				if !slices.Contains(field.Enum, v) {
					t.Errorf("%s/%s: valid value %q is not in the declared enum %v", kind, field.JSON, v, field.Enum)
				}
			}
		}
	}

	numeric := []struct {
		name  string
		json  string
		value float64
		edit  func(*engine.Provider)
	}{
		{"context_tokens below its minimum", "context_tokens", 0, func(p *engine.Provider) { p.ContextTokens = 0 }},
		{"chars_per_token at zero", "chars_per_token", 0, func(p *engine.Provider) { p.CharsPerToken = 0 }},
		{"reserve_tokens_per_byte above its maximum", "reserve_tokens_per_byte", 5, func(p *engine.Provider) { p.ReserveTokensPerByte = 5 }},
		{"max_questions below its minimum", "max_questions", 0, func(p *engine.Provider) { p.MaxQuestions = 0 }},
		{"probability_tolerance at one", "probability_tolerance", 1, func(p *engine.Provider) { p.ProbabilityTolerance = 1 }},
		{"price_input below zero", "price_input_per_mtok", -1, func(p *engine.Provider) {
			v := -1.0
			p.PriceInput = &v
		}},
		{"price_output below zero", "price_output_per_mtok", -1, func(p *engine.Provider) {
			v := -1.0
			p.PriceOutput = &v
		}},
	}
	for _, tc := range numeric {
		t.Run(tc.name, func(t *testing.T) {
			p := records[engine.KindDecider]
			tc.edit(&p)
			if _, err := engine.BuildCatalog(cfg, []engine.Provider{p}); err == nil {
				t.Fatal("the validator accepted the record")
			}
			field := schemaByJSON(t, tc.json)
			if field.Bounds == nil {
				t.Fatalf("%s declares no bounds", tc.json)
			}
			if inBounds(field.Bounds, tc.value) {
				t.Fatalf("%s: %v is inside the declared bounds %+v", tc.json, tc.value, *field.Bounds)
			}
		})
	}

	enums := []struct {
		name  string
		kind  engine.Kind
		json  string
		value string
		edit  func(*engine.Provider)
	}{
		{"unknown protocol", engine.KindDecider, "protocol", "oracle", func(p *engine.Provider) { p.Protocol = "oracle" }},
		{"unknown response format", engine.KindGenerator, "response_format", "yaml", func(p *engine.Provider) { p.ResponseFormat = "yaml" }},
		{"unknown reasoning effort", engine.KindGenerator, "reasoning_effort", "turbo", func(p *engine.Provider) { p.ReasoningEffort = "turbo" }},
		{"unknown envelope", engine.KindDecider, "envelope", "akamai", func(p *engine.Provider) { p.Envelope = "akamai" }},
	}
	for _, tc := range enums {
		t.Run(tc.name, func(t *testing.T) {
			p := records[tc.kind]
			tc.edit(&p)
			if _, err := engine.BuildCatalog(cfg, []engine.Provider{p}); err == nil {
				t.Fatal("the validator accepted the record")
			}
			field := schemaByJSON(t, tc.json)
			if len(field.Enum) == 0 {
				t.Fatalf("%s declares no enum", tc.json)
			}
			if slices.Contains(field.Enum, tc.value) {
				t.Fatalf("%s: %q is in the declared enum %v", tc.json, tc.value, field.Enum)
			}
		})
	}
}

// Every protocol the validator accepts is in the protocol enum, and the enum
// never invents one.
func TestProviderSchemaProtocolEnumIsTheProtocolTable(t *testing.T) {
	field := schemaByJSON(t, "protocol")
	var names []string
	for _, protocol := range engine.Protocols() {
		names = append(names, protocol.Name)
	}
	if !slices.Equal(field.Enum, names) {
		t.Fatalf("protocol enum %v, protocols %v", field.Enum, names)
	}
	cfg := configtest.New(t, nil)
	p := recordOf(t, "dec", deciderJSON)
	p.Protocol = "oracle"
	if _, err := engine.BuildCatalog(cfg, []engine.Provider{p}); err == nil {
		t.Fatal("an unknown protocol must be rejected")
	}
}

// The schema is served read-only: mutating one answer cannot change the next
// one, and every field carries a stable JSON key.
func TestProviderSchemaIsStable(t *testing.T) {
	first, second := engine.ProviderFields(), engine.ProviderFields()
	if !reflect.DeepEqual(first, second) {
		t.Fatal("two reads of the schema differ")
	}
	first[0].Kinds[0] = engine.Kind("mutated")
	if got := engine.ProviderFields()[0].Kinds[0]; got == engine.Kind("mutated") {
		t.Fatal("the schema shares its kinds slice between callers")
	}
	var bounds *engine.ProviderBounds
	for i := range first {
		if first[i].JSON == "probability_tolerance" {
			bounds = first[i].Bounds
			*bounds.Max = 2
		}
	}
	if bounds == nil {
		t.Fatal("probability_tolerance has no bounds")
	}
	if got := schemaByJSON(t, "probability_tolerance").Bounds.Max; *got != 1 {
		t.Fatalf("the schema shares its bounds with callers: max %v", *got)
	}
}
