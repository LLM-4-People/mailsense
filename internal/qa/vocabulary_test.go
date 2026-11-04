package qa

import (
	"sort"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// TestVocabulariesDeriveFromTheirOwner keeps the shared question vocabulary in
// one place: the entity stopwords must carry every summary verb (a new
// spelling taught to the matcher must also be filtered out of a person name),
// and the default desk's probe order must be exactly the set used for the
// membership fallback.
func TestVocabulariesDeriveFromTheirOwner(t *testing.T) {
	for _, verb := range summaryVerbList {
		if !entityStopwords[verb] {
			t.Errorf("summary verb %q is not an entity stopword", verb)
		}
	}
	if len(summaryVerbs) != len(summaryVerbList) {
		t.Errorf("summaryVerbs has %d entries, the list has %d", len(summaryVerbs), len(summaryVerbList))
	}

	desks := append([]string(nil), defaultContactDesks...)
	sort.Strings(desks)
	if got, want := strings.Join(desks, ","), strings.Join(sortedKeys(defaultContactLocals), ","); got != want {
		t.Errorf("default desk order %v and set %v disagree", got, want)
	}
	if len(defaultContactDesks) != len(defaultContactLocals) {
		t.Errorf("default desk has %d entries, the set has %d", len(defaultContactDesks), len(defaultContactLocals))
	}
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// TestOneAddressNormalizationInThePersonLeg: the person evidence keys an
// address the same way the store compares it, so a padded or upper-case
// address from a search hit still matches the message's sender.
func TestOneAddressNormalizationInThePersonLeg(t *testing.T) {
	for _, raw := range []string{"Amelie.Durand@example.org", "  amelie.durand@example.org  ", "AMELIE.DURAND@EXAMPLE.ORG"} {
		if got := mail.NormalizeAddress(raw); got != "amelie.durand@example.org" {
			t.Fatalf("NormalizeAddress(%q) = %q", raw, got)
		}
	}
	// The person leg and personSender now share this owner, so the key built
	// from a hit and the lookup key built from a stored sender are identical.
	addresses := map[int64]map[string]bool{
		1: {mail.NormalizeAddress("  Amelie.Durand@example.org "): true},
	}
	if !personSender(addresses[1], "AMELIE.DURAND@example.org") {
		t.Fatal("a padded, differently-cased hit did not match the stored sender")
	}
	if personSender(addresses[1], "someone@example.org") {
		t.Fatal("a different sender matched")
	}
}
