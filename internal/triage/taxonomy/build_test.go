package taxonomy

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

func TestProfileCountRefusesOutOfRange(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value int64
		ok    bool
	}{
		{"zero", 0, true},
		{"ordinary", 2000, true},
		{"upper bound", maxProfileInt, true},
		{"negative", -1, false},
		{"above int", int64(maxProfileInt) + 1, false},
		{"int64 max", 1<<63 - 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := profileCount(tc.value, "field")
			if tc.ok {
				if err != nil || int64(got) != tc.value {
					t.Fatalf("profileCount(%d) = %d, %v", tc.value, got, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "field") {
				t.Fatalf("profileCount(%d) = %d, %v; want a field-named error", tc.value, got, err)
			}
		})
	}
}

func TestBuildContextRefusesMaxExamplesBeyondInt(t *testing.T) {
	ctx := jsonx.Object{{Key: "max_examples", Value: json.Number("2147483648")}}
	if err := buildContext(&Profile{}, ctx); err == nil || !strings.Contains(err.Error(), "max_examples") {
		t.Fatalf("want a max_examples error, got %v", err)
	}
}

func TestParseRefusesMaxExamplesBeyondInt(t *testing.T) {
	if _, err := Parse([]byte(`{"context":{"max_examples": 2147483648}}`), "test"); err == nil || !strings.Contains(err.Error(), "max_examples") {
		t.Fatalf("want a max_examples error, got %v", err)
	}
}
