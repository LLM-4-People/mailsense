package action

import (
	"slices"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/threadintel"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// TestBandsAreOneRule pins the thresholds and their composition. The ranking's
// reasons, its filter buckets and this package's due source all read these
// levels, so an edge must be defined once: a top urgency is "urgent", a top
// deadline is "deadline soon" and therefore also enters the urgent bucket, and
// two effort or two requests are work.
func TestBandsAreOneRule(t *testing.T) {
	cases := []struct {
		name                             string
		urgency, deadline, effort, count int
		wantUrgencyHigh                  bool
		wantDeadline, wantWorkload       bool
	}{
		{name: "nothing", urgency: 0, deadline: 0},
		{name: "urgency below the top", urgency: 2},
		{name: "urgency top", urgency: 3, wantUrgencyHigh: true},
		{name: "deadline below the top", deadline: 2},
		{name: "deadline top", deadline: 3, wantDeadline: true},
		{name: "effort one", effort: 1},
		{name: "effort two", effort: 2, wantWorkload: true},
		{name: "requests two", count: 2, wantWorkload: true},
		{name: "everything", urgency: 4, deadline: 5, effort: 3, count: 3,
			wantUrgencyHigh: true, wantDeadline: true, wantWorkload: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := Bands{Urgency: tc.urgency, Deadline: tc.deadline, Effort: tc.effort, RequestCount: tc.count}
			if got := b.UrgencyHigh(); got != tc.wantUrgencyHigh {
				t.Fatalf("UrgencyHigh %v, want %v", got, tc.wantUrgencyHigh)
			}
			if got := b.DeadlineSoon(); got != tc.wantDeadline {
				t.Fatalf("DeadlineSoon %v, want %v", got, tc.wantDeadline)
			}
			if got := b.Workload(); got != tc.wantWorkload {
				t.Fatalf("Workload %v, want %v", got, tc.wantWorkload)
			}
			// The urgent bucket is the composition, never a third rule.
			if got, want := b.Urgent(), tc.wantUrgencyHigh || tc.wantDeadline; got != want {
				t.Fatalf("Urgent %v, want %v", got, want)
			}
		})
	}
}

// TestSignalsBandsReadTheSameLevels keeps the classifier's own record and the
// ranking's conversion in step: Signals.Bands must carry exactly the four
// levels the rule reads.
func TestSignalsBandsReadTheSameLevels(t *testing.T) {
	sig := Signals{Urgency: 1, Deadline: 2, Effort: 3, RequestCount: 4}
	want := Bands{Urgency: 1, Deadline: 2, Effort: 3, RequestCount: 4}
	if got := sig.Bands(); got != want {
		t.Fatalf("Bands %+v, want %+v", got, want)
	}
}

// TestDueSourceFollowsTheDeadlineBand: the classifier's due source is the
// shared band, not a local level check.
func TestDueSourceFollowsTheDeadlineBand(t *testing.T) {
	for level := range 6 {
		got := withDue(Class{Primary: Read}, Input{Signals: Signals{Deadline: level}}, false)
		wantClassifier := Signals{Deadline: level}.Bands().DeadlineSoon()
		if (got.DueSource == DueClassifier) != wantClassifier {
			t.Fatalf("deadline %d: due source %q, want classifier %v", level, got.DueSource, wantClassifier)
		}
		if wantClassifier && got.DueLevel != level {
			t.Fatalf("deadline %d: due level %d", level, got.DueLevel)
		}
		if got := withDue(Class{Primary: Read}, Input{Signals: Signals{Deadline: level}}, true); got.DueSource != "" {
			t.Fatalf("bulk mail at deadline %d got due source %q", level, got.DueSource)
		}
	}
}

// asksForOptions is every option of the default profile's asks_for question:
// the one vocabulary the classifier, the resolver and the ranking read.
func asksForOptions(t *testing.T) []string {
	t.Helper()
	p, err := taxonomy.Default()
	if err != nil {
		t.Fatal(err)
	}
	q, ok := p.Question("asks_for")
	if !ok {
		t.Fatal("the default profile has no asks_for question")
	}
	names := make([]string, 0, len(q.Options))
	for _, option := range q.Options {
		names = append(names, option.Name)
	}
	if len(names) == 0 {
		t.Fatal("asks_for has no options")
	}
	return names
}

// TestAskActionCoversEveryProfileOption: every ask the profile can produce has
// exactly one owner-facing action, and the "concrete" flag is the resolver's
// structural meaning (threadintel.AskWork). A new profile option that nobody
// classified would fall to Read without a test noticing.
func TestAskActionCoversEveryProfileOption(t *testing.T) {
	want := map[string]struct {
		primary  Primary
		concrete bool
	}{
		"answer_question":      {Reply, false},
		"approval_decision":    {Decide, true},
		"information_document": {Send, true},
		"payment_money":        {Pay, true},
		"meeting_availability": {Schedule, true},
		"task_work":            {Do, true},
		"nothing_fyi":          {Read, false},
		"other":                {Read, false},
	}
	for _, option := range asksForOptions(t) {
		expect, ok := want[option]
		if !ok {
			t.Fatalf("asks_for option %q has no reviewed action; classify it here first", option)
		}
		primary, concrete := AskAction(option)
		if primary != expect.primary || concrete != expect.concrete {
			t.Fatalf("%q -> %q concrete=%v, want %q concrete=%v", option, primary, concrete, expect.primary, expect.concrete)
		}
		if got, wantKind := concrete, threadintel.ClassifyAsk(option) == threadintel.AskWork; got != wantKind {
			t.Fatalf("%q concrete=%v but the resolver's kind says %v", option, got, wantKind)
		}
		// An unrecognized value is never concrete and never invents an action.
		if primary, concrete := AskAction(""); primary != Read || concrete {
			t.Fatalf("empty ask -> %q concrete=%v", primary, concrete)
		}
	}
	// Every reviewed option in the table is a real profile option.
	options := asksForOptions(t)
	for option := range want {
		if !slices.Contains(options, option) {
			t.Errorf("reviewed ask %q is not in the profile", option)
		}
	}
}

// TestClassifierUsesTheAskTable: primaryFor reads the same table, so its
// reason names the ask it classified and its action is the table's.
func TestClassifierUsesTheAskTable(t *testing.T) {
	for _, option := range asksForOptions(t) {
		primary, _ := AskAction(option)
		got, reasons := primaryFor(Signals{AsksFor: option}, false)
		kind := threadintel.ClassifyAsk(option)
		if kind == threadintel.AskNone {
			if got != Read {
				t.Fatalf("%q fell through to %q, want Read", option, got)
			}
			continue
		}
		if got != primary {
			t.Fatalf("%q classified as %q, want %q (reasons %v)", option, got, primary, reasons)
		}
		if kind == threadintel.AskFYI {
			if !slices.Contains(reasons, "fyi") {
				t.Fatalf("%q lacks the fyi reason: %v", option, reasons)
			}
		} else if !slices.Contains(reasons, "ask:"+option) {
			t.Fatalf("%q lacks its ask reason: %v", option, reasons)
		}
	}
}
