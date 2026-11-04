package qa

import (
	"os"
	"strings"
	"testing"
)

// TestSummaryIntentDerivesFromTheVerbList: the summary-intent vocabulary of a
// "today" question is the one summary verb list plus the summary nouns that are
// not verbs. A second literal list is how a French spelling became a summary
// request for the topic path and a freeform question for the today path.
func TestSummaryIntentDerivesFromTheVerbList(t *testing.T) {
	intent, err := os.ReadFile("intent.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(intent), "IntentSummaryToday: {") {
		t.Error("intent.go spells the summary-intent list itself; derive it from summaryVerbList")
	}
	if !strings.Contains(string(intent), "IntentSummaryToday: summaryIntentWords,") {
		t.Error("intent.go does not read the derived summary-intent vocabulary")
	}
	for _, verb := range summaryVerbList {
		if !containsString(summaryIntentWords, verb) {
			t.Errorf("summaryIntentWords lacks the summary verb %q", verb)
		}
		if !summaryVerbs[verb] {
			t.Errorf("summaryVerbs lacks the summary verb %q", verb)
		}
	}
	for _, noun := range []string{"bilan", "overview", "brief"} {
		if !containsString(summaryIntentWords, noun) {
			t.Errorf("summaryIntentWords lacks the summary noun %q", noun)
		}
	}
	// The derived list is pinned by sample: a derivation test that only walks
	// summaryVerbList cannot see a spelling removed from it.
	for _, spelling := range []string{"resumez", "resumer", "synthetise", "recapitule", "summary", "brief"} {
		if !containsString(summaryIntentWords, spelling) {
			t.Errorf("summaryIntentWords lacks the pinned spelling %q", spelling)
		}
	}
}

// TestSummaryIntentReachesEverySpelling: a spelling the verb list teaches is a
// summary request in a today question, not only in a topic question.
func TestSummaryIntentReachesEverySpelling(t *testing.T) {
	for _, c := range []struct {
		question string
		want     string
	}{
		{"résumez les échanges d'aujourd'hui", IntentSummaryToday},
		{"résumer today's messages", IntentSummaryToday},
		{"synthétise ma journée", IntentSummaryToday},
		{"overview of today's mail", IntentSummaryToday},
		{"recapitule la journee", IntentSummaryToday},
	} {
		if got, _ := parseIntent(c.question); got != c.want {
			t.Errorf("parseIntent(%q) = %q, want %q", c.question, got, c.want)
		}
	}
	if got, _ := parseIntent("combien de messages la semaine dernière"); got == IntentSummaryToday {
		t.Error("a count question about another week became a summary request")
	}
}

// containsString reports whether list holds value.
func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
