package config

import (
	"strings"
	"testing"
)

// Shared value lists live once in the schema (vocabularies) and are
// referenced by one_of:<name> and keyed:<name>; misuse is refused.
func TestVocabularyFormats(t *testing.T) {
	doc := func(format, def string) string {
		return `{"vocabularies": {"role": ["inbox", "sent"]}, "settings": [{"key": "a.b", "type": "list", "item_type": "string",
			"format": "` + format + `", "default": ` + def + `, "group": "G", "description": "d"}]}`
	}
	if _, err := ParseSchema([]byte(doc("one_of:role", `["inbox"]`))); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseSchema([]byte(doc("keyed:role", `["Projets/2026=sent"]`))); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string][2]string{
		"unknown vocabulary": {"one_of:nope", `[]`},
		"value outside":      {"one_of:role", `["junk"]`},
		"keyed without =":    {"keyed:role", `["inbox"]`},
		"keyed empty name":   {"keyed:role", `["=inbox"]`},
		"keyed bad value":    {"keyed:role", `["x=spam"]`},
	} {
		if _, err := ParseSchema([]byte(doc(c[0], c[1]))); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	s, _ := Embedded()
	if v := s.Vocabulary("folder_role"); len(v) == 0 || !strings.Contains(strings.Join(v, ","), "junk") {
		t.Fatalf("the shipped vocabulary: %v", v)
	}
	if s.Vocabulary("nope") != nil {
		t.Fatal("an unknown vocabulary is nil")
	}
}
