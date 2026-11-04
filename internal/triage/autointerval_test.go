package triage

import (
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
)

// TestAutoIntervalFollowsTheSetting: the catch-up interval is the schema's value
// by default and the live override afterwards, never a number in code.
func TestAutoIntervalFollowsTheSetting(t *testing.T) {
	def, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	want := def.Duration(SettingInterval)
	if want <= 0 {
		t.Fatalf("the schema has no positive %s default (got %s)", SettingInterval, want)
	}
	base := configtest.New(t, nil)
	if got := AutoInterval(base); got != want {
		t.Fatalf("AutoInterval = %s, want the schema default %s", got, want)
	}
	live := configtest.New(t, map[string]any{SettingInterval: "90s"})
	if got := AutoInterval(live); got != 90*time.Second {
		t.Fatalf("AutoInterval = %s, want the live 90s", got)
	}
}
