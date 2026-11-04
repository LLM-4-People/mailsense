package feedback

import (
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// French accents, decomposable Unicode and non-Latin scripts all produce
// one token; the ASCII-only legacy regex produced "union" and "curit".
func TestTokensFoldAccentsAndKeepScripts(t *testing.T) {
	got := tokens("Réunion sécurité — 東京 VPN IT de")
	for _, want := range []string{"reunion", "securite", "東京", "vpn"} {
		if !got[want] {
			t.Fatalf("token %q missing from %v", want, got)
		}
	}
	for _, unwanted := range []string{"union", "curit", "it", "de"} {
		if got[unwanted] {
			t.Fatalf("noise token %q in %v", unwanted, got)
		}
	}
}

// A label whose subject carries the accents still matches a message whose
// subject was typed without them, so French and IT vocabulary is not lost
// at the few-shot boundary.
func TestSelectExamplesMatchesFoldedUnicodeSubject(t *testing.T) {
	ls := []Label{at(label(1, "category", engine.Choice, taxonomy.Label{Choice: "work"},
		"a@x.com", "Réunion budget", ""), 1)}
	got := SelectExamples(ls, 9, "a@x.com", "reunion budget", day.Add(10*time.Hour), 10, mail.NewDomains(nil))
	if len(got) != 1 || got[0].MessageID != 1 || got[0].Value.Choice != "work" {
		t.Fatalf("folded subject did not select the label: %+v", got)
	}
}
