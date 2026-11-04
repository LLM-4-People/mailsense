package evidence

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

const evidenceDoc = "../../docs/EVIDENCE.md"

// referenceMarker starts the generated part of docs/EVIDENCE.md: the
// signal and rule reference, written from the registry and rules.json.
const referenceMarker = "## Signal reference\n"

// TestTheDocReferenceIsGenerated: the reference part of docs/EVIDENCE.md
// is exactly what the registry and the shipped rules give.
// EVIDENCE_DOC_WRITE=1 rewrites it (then review the diff).
func TestTheDocReferenceIsGenerated(t *testing.T) {
	raw, err := os.ReadFile(evidenceDoc)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	i := strings.Index(doc, referenceMarker)
	if i < 0 {
		t.Fatalf("%s has no %q", evidenceDoc, strings.TrimSpace(referenceMarker))
	}
	want := doc[:i] + reference(t)
	if want == doc {
		return
	}
	if os.Getenv("EVIDENCE_DOC_WRITE") != "" {
		if err := os.WriteFile(evidenceDoc, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s rewritten", evidenceDoc)
		return
	}
	t.Errorf("%s: the reference differs from the registry: run EVIDENCE_DOC_WRITE=1 go test -run TestTheDocReferenceIsGenerated ./internal/evidence", evidenceDoc)
}

// reference renders the signal tables (per group) and the rule table.
func reference(t *testing.T) string {
	var b strings.Builder
	b.WriteString(referenceMarker)
	for _, g := range []string{GroupHeaders, GroupRecipients, GroupThread, GroupHistory, GroupContent, GroupOwner} {
		fmt.Fprintf(&b, "\n### %s\n\n| Signal | Type | Needs | Causal | Context | Meaning |\n| --- | --- | --- | --- | --- | --- |\n", g)
		for _, s := range Signals() {
			if s.Group == g {
				fmt.Fprintf(&b, "| `%s` v%d | %s | %s | %s | %s | %s |\n", s.Name, s.Version, typeText(s), needsText(s), yes(s.Causal), yes(s.Context), s.Doc)
			}
		}
	}
	set, err := parseRules(rulesJSON, Signals())
	if err != nil {
		t.Fatal(err)
	}
	b.WriteString("\n## Rule reference\n\n| Rule | Question | Answer | When |\n| --- | --- | --- | --- |\n")
	for _, r := range set.Rules {
		var when []string
		for _, c := range r.When {
			when = append(when, condText(c))
		}
		fmt.Fprintf(&b, "| `%s` | %s | `%s` | %s |\n", r.ID, r.Question, r.Answer, strings.Join(when, " and "))
	}
	return b.String()
}

func typeText(s Signal) string {
	switch {
	case s.Type != TypeEnum:
		return string(s.Type)
	case len(s.Values) > 16:
		return fmt.Sprintf("enum (%d values, from the pinned data)", len(s.Values))
	}
	return "enum: " + strings.Join(s.Values, ", ")
}

func needsText(s Signal) string {
	out := strings.Join(s.Needs, ", ")
	if len(s.AnyOf) > 0 {
		out = strings.TrimPrefix(out+", any of "+strings.Join(s.AnyOf, ", "), ", ")
	}
	return out
}

func yes(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func condText(c Cond) string {
	switch {
	case c.Is != nil:
		return fmt.Sprintf("%s = %s", c.Signal, strings.Trim(string(c.Is), `"`))
	case c.In != nil:
		return fmt.Sprintf("%s ∈ {%s}", c.Signal, strings.Join(c.In, ", "))
	case c.Has != "":
		return fmt.Sprintf("%s ∋ %s", c.Signal, c.Has)
	}
	out := c.Signal
	if c.Min != nil {
		out += fmt.Sprintf(" ≥ %v", *c.Min)
	}
	if c.Max != nil {
		out += fmt.Sprintf(" ≤ %v", *c.Max)
	}
	return out
}
