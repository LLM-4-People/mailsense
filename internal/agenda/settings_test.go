package agenda

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config"
)

// TestDefaultHoursSettingIsTheSchemaKey: the constant names a real schema key,
// so a typo cannot silently turn the horizon into zero hours.
func TestDefaultHoursSettingIsTheSchemaKey(t *testing.T) {
	def, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	if hours := def.Int(SettingDefaultHours); hours <= 0 {
		t.Fatalf("the schema has no positive %s default (got %d)", SettingDefaultHours, hours)
	}
}
