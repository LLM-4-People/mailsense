package priority

import (
	"os"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/action"
)

// TestDecisionsReadsTheOnePredicate: the Decisions view and the Decision filter
// answer the same question. A second expression is how the view starts showing
// rows the filter would hide.
func TestDecisionsReadsTheOnePredicate(t *testing.T) {
	data, err := os.ReadFile("priority.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	start := strings.Index(source, "func (s *Service) Decisions(")
	if start < 0 {
		t.Fatal("Decisions not found")
	}
	end := strings.Index(source[start:], "\n}\n")
	if end < 0 {
		t.Fatal("Decisions has no body end")
	}
	body := source[start : start+end]
	if !strings.Contains(body, "FilterDecision.matches(") {
		t.Error("Decisions builds its own decision predicate")
	}
	if strings.Contains(body, "action.Decide &&") {
		t.Error("Decisions re-spells the decision predicate")
	}
	if got := strings.Count(source, "replyOpen"); got != 0 {
		t.Errorf("priority.go keeps the duplicate replyOpen field %d times", got)
	}
}

// TestDecisionFilterMatchesOnlyTheOwnersPendingDecision: the filter accepts an
// owner-facing decision and rejects a decision waiting on the other side.
func TestDecisionFilterMatchesOnlyTheOwnersPendingDecision(t *testing.T) {
	decide := func(waiting action.Waiting) Item {
		return Item{Action: action.Class{Primary: action.Decide, Waiting: waiting}}
	}
	for _, c := range []struct {
		name string
		item Item
		want bool
	}{
		{"owner owes a decision", decide(action.WaitingOwner), true},
		{"other side owes it", decide(action.WaitingOther), false},
		{"nothing pending", decide(action.WaitingNone), false},
		{"not a decision", Item{Action: action.Class{Primary: action.Reply, Waiting: action.WaitingOwner}}, false},
	} {
		if got := FilterDecision.matches(c.item); got != c.want {
			t.Errorf("%s: matches = %v, want %v", c.name, got, c.want)
		}
	}
}
