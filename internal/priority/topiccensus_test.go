package priority

import "testing"

// TestTopicCensusLookups pins the two census lookups a topic answer reads:
// an absent topic or subtopic is reported as absent (a real zero), never as
// a fabricated row.
func TestTopicCensusLookups(t *testing.T) {
	census := TopicCensus{Classified: 5, Scanned: 9, Topics: []TopicCount{
		{Topic: "project_work", Count: 3, Evidence: []int64{7, 6, 5},
			Subtopics: []SubtopicCount{
				{Subtopic: "status_update", Count: 2, Evidence: []int64{7, 6}},
				{Subtopic: "deliverable", Count: 1, Evidence: []int64{5}},
			}},
		{Topic: "billing_account", Count: 2},
	}}

	if row, ok := census.Topic("project_work"); !ok || row.Count != 3 || len(row.Subtopics) != 2 {
		t.Fatalf("project_work = %+v, %v", row, ok)
	}
	if _, ok := census.Topic("hiring"); ok {
		t.Fatal("an absent topic reported a row")
	}
	project, _ := census.Topic("project_work")
	if sub, ok := project.Subtopic("status_update"); !ok || sub.Count != 2 || sub.Evidence[0] != 7 {
		t.Fatalf("status_update = %+v, %v", sub, ok)
	}
	if _, ok := project.Subtopic("approval"); ok {
		t.Fatal("an absent subtopic reported a row")
	}
	billing, _ := census.Topic("billing_account")
	if _, ok := billing.Subtopic("status_update"); ok {
		t.Fatal("a subtopic of another topic reported a row")
	}
}
