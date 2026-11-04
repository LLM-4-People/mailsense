package qa

import (
	"os"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/search"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// TestRetrievalProblemClassifiesTheSemanticLeg: one classifier turns a semantic
// state into the sentinel the notice mapping owns, and a timeout wins over a
// partial vector copy.
func TestRetrievalProblemClassifiesTheSemanticLeg(t *testing.T) {
	for _, c := range []struct {
		name string
		sem  search.Semantic
		want error
	}{
		{"complete", search.Semantic{Used: true, Covered: 4, Eligible: 4}, nil},
		{"no eligible mail", search.Semantic{Used: false, Covered: 0, Eligible: 0}, nil},
		{"partial copy", search.Semantic{Used: true, Covered: 1, Eligible: 5}, errSearchWarming},
		{"timeout", search.Semantic{TimedOut: true, Covered: 5, Eligible: 5}, errSearchTimeout},
		{"timeout on a partial copy", search.Semantic{TimedOut: true, Covered: 1, Eligible: 5}, errSearchTimeout},
	} {
		if got := retrievalProblemOf(c.sem); got != c.want {
			t.Errorf("%s: retrievalProblemOf = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestTopicDegradationReportsAPartialIndex: the topic lane used to report every
// degradation as a timeout and to ignore a partially covered vector set, so a
// warming index looked like a normal, complete answer.
func TestTopicDegradationReportsAPartialIndex(t *testing.T) {
	w := newWorld(t, false)
	w.store.people = []store.PersonCandidate{
		{Address: "nadia.tremblay@atelier-nord.test", Name: "Nadia Tremblay", Messages: 10},
	}
	question := "donne-moi la liste des clients qui parlent d'IA"
	topic := "IA intelligence artificielle"
	w.gen.content = `{"route":"plan","plan":{"kind":"contacts","action":"rank","direction":"received","metric":"messages","text":"` + topic + `","time_range":"all","limit":50,"order":"desc"}}`
	w.search.results = map[string]search.Result{
		question: {},
		topic: {Hits: []search.Hit{
			{Summary: store.Summary{RowID: 11, SenderName: "Ada Lovelace", SenderAddress: "ada@example.com",
				Subject: "AI note", Instant: "2026-10-20T10:00:00Z", Role: mail.RoleInbox}},
		}, Semantic: search.Semantic{Used: true, Covered: 1, Eligible: 5}},
	}
	view, err := w.svc.Ask(t.Context(), question, "fr")
	if err != nil {
		t.Fatal(err)
	}
	if view.Notice != NoticeIndexWarming {
		t.Fatalf("topic notice = %q, want %q; a partial copy was reported as a complete answer",
			view.Notice, NoticeIndexWarming)
	}
}

// TestTopicNoticeComesFromTheOneMapping: the topic lane maps its degradation
// through retrievalNotice, and every caller of a semantic state classifies it
// with retrievalProblemOf.
func TestTopicNoticeComesFromTheOneMapping(t *testing.T) {
	data, err := os.ReadFile("topic.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	if !strings.Contains(source, "retrievalNotice(err)") {
		t.Error("topic.go does not map its degradation through retrievalNotice")
	}
	if strings.Contains(source, "NoticeRetrievalTimeout") {
		t.Error("topic.go hardcodes the timeout notice instead of mapping the sentinel")
	}
	if !strings.Contains(source, "retrievalProblemOf(res.Semantic)") {
		t.Error("topic.go does not read the shared semantic classifier")
	}
	ask, err := os.ReadFile("ask.go")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(ask), "func retrievalProblemOf("); got != 1 {
		t.Errorf("ask.go defines retrievalProblemOf %d times, want once", got)
	}
	if strings.Contains(string(ask), "retrievalProblem ") {
		t.Error("ask.go still declares the string problem type beside the sentinels")
	}
}
