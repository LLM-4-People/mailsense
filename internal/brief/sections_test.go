package brief

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/commitments"
	"github.com/LLM-4-People/mailsense/internal/conversations"
	"github.com/LLM-4-People/mailsense/internal/priority"
)

// TestEverySectionCountsAtLeastWhatItShows: a section reports at least the rows
// it carries. A source can under-report (a per-direction count map with no
// entry, a filtered list with no total) and the UI's "1-3 of N" arithmetic
// breaks when Count is below len(Items). Each builder used to restate the rule
// in its own shape; sectionCount is the one place.
func TestEverySectionCountsAtLeastWhatItShows(t *testing.T) {
	owner := func(id string) commitments.Commitment {
		return commitments.Commitment{ID: id, Kind: commitments.KindDue, Direction: commitments.DirectionOwner,
			Status: commitments.StatusOpen, Title: id, Subject: id, Evidence: []int64{1}}
	}
	loopsOrg := func(key string) commitments.OrganizationOpenLoops {
		return commitments.OrganizationOpenLoops{Key: key, Label: key, Total: 1, Owner: 1}
	}
	fact := func(id string) conversations.Fact {
		return conversations.Fact{ConversationID: id, LastMessageID: 1}
	}
	svc := &Service{priority: &fakePriority{census: priority.TopicCensus{
		Classified: 0, // under-reported: one topic is shown
		Topics:     []priority.TopicCount{{Topic: "invoices", Count: 1, Evidence: []int64{1}}},
	}}}

	cases := []struct {
		name    string
		section Section
		want    int // -1 means "only the invariant", an honest total is checked separately
	}{
		{name: "commitments without a direction count",
			section: commitmentSection(commitments.Result{Items: []commitments.Commitment{owner("a"), owner("b")}}, commitments.DirectionOwner, 10), want: -1},
		{name: "recent without a total",
			section: changedSection(conversations.Facts{Recent: []conversations.Fact{fact("C1"), fact("C2")}}, ""), want: -1},
		{name: "relationships without a total",
			section: relationshipSection(commitments.OrganizationOpenLoopsResult{
				Organizations: []commitments.OrganizationOpenLoops{loopsOrg("acme"), loopsOrg("beta")}}), want: -1},
		{name: "topics without a classified count",
			section: svc.topicSection(t.Context(), 0), want: -1},
		{name: "commitments with an honest total",
			section: commitmentSection(commitments.Result{Items: []commitments.Commitment{owner("a"), owner("b")},
				Counts: map[string]int{string(commitments.DirectionOwner): 5}}, commitments.DirectionOwner, 10), want: 5},
		{name: "recent with an honest total",
			section: changedSection(conversations.Facts{Recent: []conversations.Fact{fact("C1")}, RecentTotal: 4}, ""), want: 4},
		{name: "relationships with an honest total",
			section: relationshipSection(commitments.OrganizationOpenLoopsResult{
				Total:         7,
				Organizations: []commitments.OrganizationOpenLoops{loopsOrg("acme")}}), want: 7},
		{name: "topics with an honest count",
			section: (&Service{priority: &fakePriority{census: priority.TopicCensus{
				Classified: 9, Topics: []priority.TopicCount{{Topic: "invoices", Count: 3}}}}}).topicSection(t.Context(), 9), want: 9},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			shown := len(tc.section.Items)
			if shown == 0 {
				t.Fatal("the case must show at least one row")
			}
			if tc.section.Count < shown {
				t.Fatalf("count %d below the %d rows shown", tc.section.Count, shown)
			}
			if tc.want >= 0 && tc.section.Count != tc.want {
				t.Fatalf("count %d, want the honest total %d", tc.section.Count, tc.want)
			}
			if tc.section.Total != 0 && tc.section.Total < tc.section.Count {
				t.Fatalf("total %d below count %d", tc.section.Total, tc.section.Count)
			}
		})
	}
}
