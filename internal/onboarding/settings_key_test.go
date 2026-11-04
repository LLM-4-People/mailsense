package onboarding

import (
	"os"
	"strings"
	"testing"
)

// TestEmbedProviderKeyIsSpelledOnce: the read and the write of the embedder
// setting share one constant, so a typo cannot make Ensure read one key and
// write another.
func TestEmbedProviderKeyIsSpelledOnce(t *testing.T) {
	data, err := os.ReadFile("onboarding.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	if got := strings.Count(source, `"embed.provider"`); got != 1 {
		t.Fatalf(`onboarding.go spells "embed.provider" %d times, want only the constant`, got)
	}
	if !strings.Contains(source, "snap.String(settingEmbedProvider)") ||
		!strings.Contains(source, "cfg.Set(settingEmbedProvider, p.ID)") {
		t.Fatal("onboarding does not read and write the setting through settingEmbedProvider")
	}
}
