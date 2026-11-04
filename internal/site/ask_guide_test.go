package site

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/prose"
)

// TestAskGuideFollowsThePublicVoice applies the one prose rule set to the two
// Ask guides. The prose package's closed file list does not name them, so this
// pair is guarded here: a new public guide cannot skip the voice gate that the
// older pages pass.
func TestAskGuideFollowsThePublicVoice(t *testing.T) {
	for _, name := range []string{"docs/ask.md", "docs/fr/ask.md"} {
		body, err := os.ReadFile(filepath.Join(root(t), filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		for _, finding := range prose.CheckDocument(name, body) {
			t.Error(finding.String())
		}
	}
}
