package priority

import (
	"slices"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/action"
	"github.com/LLM-4-People/mailsense/internal/route"
)

// TestFilterBucketsAreTheScoringReasons: a filter bucket is a view of the
// ranking's own reasons, never a second classification of the same levels.
// The urgent bucket used to accept a top deadline while the scoring reason
// named only a top urgency, and the workload bucket re-typed the effort and
// request thresholds. The table drives every level edge through Score and
// requires the bucket to be exactly the composition of the reasons.
func TestFilterBucketsAreTheScoringReasons(t *testing.T) {
	// Only a level that enters the ranking can be compared with its reasons;
	// action.BandsTest covers every edge, including the ones that are too weak
	// to force an entry on their own.
	levels := []struct {
		name   string
		answer map[string]route.Answer
	}{
		{"urgency three", map[string]route.Answer{"urgency": score("urgency", 3)}},
		{"urgency four", map[string]route.Answer{"urgency": score("urgency", 4)}},
		{"deadline five", map[string]route.Answer{"deadline": score("deadline", 5)}},
		{"deadline two", map[string]route.Answer{"deadline": score("deadline", 2)}},
		{"effort three", map[string]route.Answer{"effort": score("effort", 3)}},
		{"requests three", map[string]route.Answer{"request_count": score("request_count", 3)}},
		{"workload pair", map[string]route.Answer{"effort": score("effort", 3), "request_count": score("request_count", 3)}},
	}
	has := func(reasons []string, name string) bool { return slices.Contains(reasons, name) }
	for _, tc := range levels {
		t.Run(tc.name, func(t *testing.T) {
			item, ok := scoreOne(t, tc.answer, receivedAgo(0))
			if !ok {
				t.Fatalf("%v did not enter the ranking; the bucket test needs a scored item", tc.answer)
			}
			bands := item.Signals.Bands()
			wantUrgent := has(item.Reasons, "urgent") || has(item.Reasons, "deadline")
			if got := FilterUrgent.matches(item); got != wantUrgent {
				t.Fatalf("urgent bucket %v, reasons %v", got, item.Reasons)
			}
			if got := FilterDeadline.matches(item); got != has(item.Reasons, "deadline") {
				t.Fatalf("deadline bucket %v, reasons %v", got, item.Reasons)
			}
			if got := FilterWorkload.matches(item); got != has(item.Reasons, "workload") {
				t.Fatalf("workload bucket %v, reasons %v", got, item.Reasons)
			}
			// And the reasons are the classifier's bands, not local literals.
			if has(item.Reasons, "deadline") != bands.DeadlineSoon() {
				t.Fatalf("deadline reason disagrees with the shared band rule: %v", item.Reasons)
			}
			if has(item.Reasons, "workload") != bands.Workload() {
				t.Fatalf("workload reason disagrees with the shared band rule: %v", item.Reasons)
			}
		})
	}
}

// TestDueSourceIsTheSharedDeadlineBand: the classifier's due source and the
// ranking's deadline bucket are the same top level.
func TestDueSourceIsTheSharedDeadlineBand(t *testing.T) {
	item, ok := scoreOne(t, map[string]route.Answer{"deadline": score("deadline", 5)}, receivedAgo(0))
	if !ok {
		t.Fatal("a top deadline must enter the ranking")
	}
	if !item.Signals.Bands().DeadlineSoon() {
		t.Fatalf("the ranking did not read the top deadline: %+v", item.Signals)
	}
	if item.Action.DueSource != action.DueClassifier {
		t.Fatalf("due source %q, want the classifier's deadline", item.Action.DueSource)
	}
	if item.Action.DueLevel != item.Signals.Deadline {
		t.Fatalf("due level %d, deadline level %d", item.Action.DueLevel, item.Signals.Deadline)
	}
}

// TestConcreteAskIsTheRankingsEntryRule: an explicit ask enters the ranking
// exactly when the classifier's one table calls it concrete work. The ranking
// used to keep its own list of five asks, so a new profile option could become
// an action without ever being ranked, or enter without an action.
func TestConcreteAskIsTheRankingsEntryRule(t *testing.T) {
	profile := defaultProfile(t)
	question, ok := profile.Question("asks_for")
	if !ok {
		t.Fatal("the default profile has no asks_for question")
	}
	for _, option := range question.Options {
		t.Run(option.Name, func(t *testing.T) {
			_, concrete := action.AskAction(option.Name)
			item, entered := scoreOne(t, map[string]route.Answer{"asks_for": choiceAns("asks_for", option.Name)}, receivedAgo(0))
			if entered != concrete {
				t.Fatalf("ranking entry %v for %q, want %v", entered, option.Name, concrete)
			}
			if concrete && !slices.Contains(item.Reasons, "action") {
				t.Fatalf("%q entered without the action reason: %v", option.Name, item.Reasons)
			}
			if !concrete && entered {
				t.Fatalf("%q entered the ranking without concrete work: %v", option.Name, item.Reasons)
			}
		})
	}
}
