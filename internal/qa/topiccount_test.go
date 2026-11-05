package qa

import (
	"errors"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/priority"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/subject"
)

// topicTestCensus is the census fixture of the topic-count tests: one topic
// with a subtopic hierarchy and two more topics, nine classified messages in
// a twelve-message mailbox.
func topicTestCensus() priority.TopicCensus {
	return priority.TopicCensus{Scanned: 12, Classified: 9, Topics: []priority.TopicCount{
		{Topic: "billing_account", Count: 3, Evidence: []int64{9, 8, 7},
			Subtopics: []priority.SubtopicCount{
				{Subtopic: "invoice", Count: 2, Evidence: []int64{9, 8}},
				{Subtopic: "contract", Count: 1, Evidence: []int64{7}},
			}},
		{Topic: "project_work", Count: 4, Evidence: []int64{5, 4, 3}},
		{Topic: "support_issue", Count: 2, Evidence: []int64{2, 1}},
	}}
}

// seedTopicWorld prepares the provider-less topic path: no usable generator,
// the census fixture, and one owner summary per evidence id so the citation
// round trip is observable.
func seedTopicWorld(t *testing.T) *testWorld {
	t.Helper()
	w := newWorld(t, false)
	w.svc.source = &fakeSource{err: ErrUnavailable}
	w.prio.topics = topicTestCensus()
	for _, id := range []int64{1, 2, 3, 4, 5, 7, 8, 9} {
		w.store.summaries[id] = store.Summary{RowID: id, Subject: "subject", SenderName: "sender",
			SenderAddress: "sender@example.com", Instant: "2026-10-25T10:00:00Z"}
	}
	return w
}

// TestProviderlessTopicCountAnswersFromTheCensus pins the new provider-less
// owner: a plain count of one catalogue topic or subtopic, EN and FR, is
// answered structurally from the census the ranking already built — no
// generator call, no model prose, exactly one census read — and the answer
// survives the history round trip.
func TestProviderlessTopicCountAnswersFromTheCensus(t *testing.T) {
	cases := []struct {
		name       string
		question   string
		language   string
		topic      string
		sub        string
		count      int
		evidence   []int64
		classified int
		scanned    int
	}{
		{"en invoices", "How many invoices?", "en", "billing_account", "invoice", 2, []int64{9, 8}, 9, 12},
		{"fr factures", "Combien de factures ?", "fr", "billing_account", "invoice", 2, []int64{9, 8}, 9, 12},
		{"billing whole topic", "How many billing messages?", "en", "billing_account", "", 3, []int64{9, 8, 7}, 9, 12},
		{"fr projet", "Combien de projets ?", "fr", "project_work", "", 4, []int64{5, 4, 3}, 9, 12},
		{"en support requests", "How many support requests?", "en", "support_issue", "", 2, []int64{2, 1}, 9, 12},
		{"en about invoices", "How many messages about invoices?", "en", "billing_account", "invoice", 2, []int64{9, 8}, 9, 12},
		{"fr sur les factures", "Combien de messages sur les factures ?", "fr", "billing_account", "invoice", 2, []int64{9, 8}, 9, 12},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := seedTopicWorld(t)

			view, err := w.svc.Ask(t.Context(), tc.question, tc.language)
			if err != nil {
				t.Fatal(err)
			}
			if view.Mode != ModeStructured || view.Intent != IntentTopic || view.Answer != "" {
				t.Fatalf("mode/intent/answer = %q/%q/%q; want structured/topic/\"\" (%+v)",
					view.Mode, view.Intent, view.Answer, view)
			}
			if view.Topic == nil {
				t.Fatal("the topic answer is missing")
			}
			got := *view.Topic
			if got.Topic != tc.topic || got.Subtopic != tc.sub || got.Count != tc.count ||
				got.Classified != tc.classified || got.Scanned != tc.scanned {
				t.Fatalf("topic answer = %+v, want %s/%s count %d coverage %d/%d",
					got, tc.topic, tc.sub, tc.count, tc.classified, tc.scanned)
			}
			if len(view.Citations) != len(tc.evidence) {
				t.Fatalf("citations = %+v, want ids %v", view.Citations, tc.evidence)
			}
			for i, citation := range view.Citations {
				if citation.MessageID != tc.evidence[i] || citation.Reason != "topic" {
					t.Fatalf("citation %d = %+v, want id %d reason topic", i, citation, tc.evidence[i])
				}
			}
			if w.gen.calls != 0 || w.search.calls != 0 || w.prio.calls != 0 {
				t.Fatalf("generator=%d search=%d ranking=%d, want 0/0/0", w.gen.calls, w.search.calls, w.prio.calls)
			}
			if w.prio.topicsCalls != 1 {
				t.Fatalf("census reads = %d, want 1", w.prio.topicsCalls)
			}
			if view.ID == 0 || len(w.store.saved) != 1 {
				t.Fatalf("the topic count was not persisted: id=%d rows=%d", view.ID, len(w.store.saved))
			}
			saved := w.store.saved[0]
			if saved.Mode != ModeStructured || saved.Intent != IntentTopic || saved.Answer != "" {
				t.Fatalf("saved history = %+v", saved)
			}
			if saved.Topic == nil || saved.Topic.Topic != tc.topic || saved.Topic.Subtopic != tc.sub ||
				saved.Topic.Count != tc.count || saved.Topic.Classified != tc.classified || saved.Topic.Scanned != tc.scanned {
				t.Fatalf("saved topic = %+v, want %s/%s count %d", saved.Topic, tc.topic, tc.sub, tc.count)
			}

			item, ok, err := w.svc.HistoryItem(t.Context(), view.ID)
			if err != nil || !ok {
				t.Fatalf("history item: ok=%v err=%v", ok, err)
			}
			if item.Topic == nil || *item.Topic != got {
				t.Fatalf("history topic = %+v, want %+v", item.Topic, got)
			}
			if len(item.Citations) != len(tc.evidence) {
				t.Fatalf("history citations = %+v, want ids %v", item.Citations, tc.evidence)
			}
			for i, citation := range item.Citations {
				if citation.MessageID != tc.evidence[i] || citation.Subject != "subject" {
					t.Fatalf("history citation %d = %+v", i, citation)
				}
			}
		})
	}
}

// TestProviderlessTopicCountZeroCarriesCoverage proves an empty topic is a
// disclosed zero, never a silent one: the answer carries the classification
// coverage (an unclassified mailbox is visible) and no citation, and it is
// still persisted.
func TestProviderlessTopicCountZeroCarriesCoverage(t *testing.T) {
	cases := []struct {
		name       string
		question   string
		census     priority.TopicCensus
		classified int
		scanned    int
	}{
		{"unclassified mailbox", "How many invoices?", priority.TopicCensus{Scanned: 12}, 0, 12},
		{"absent subtopic", "How many invoices?", priority.TopicCensus{Scanned: 12, Classified: 3,
			Topics: []priority.TopicCount{{Topic: "billing_account", Count: 3, Evidence: []int64{9, 8, 7}}}}, 3, 12},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, false)
			w.svc.source = &fakeSource{err: ErrUnavailable}
			w.prio.topics = tc.census

			view, err := w.svc.Ask(t.Context(), tc.question, "en")
			if err != nil {
				t.Fatal(err)
			}
			if view.Mode != ModeStructured || view.Intent != IntentTopic {
				t.Fatalf("mode/intent = %q/%q, want structured/topic", view.Mode, view.Intent)
			}
			if view.Topic == nil || view.Topic.Count != 0 {
				t.Fatalf("topic = %+v, want a real zero", view.Topic)
			}
			if view.Topic.Classified != tc.classified || view.Topic.Scanned != tc.scanned {
				t.Fatalf("coverage = %d/%d, want %d/%d",
					view.Topic.Classified, view.Topic.Scanned, tc.classified, tc.scanned)
			}
			if len(view.Citations) != 0 {
				t.Fatalf("a zero count fabricated citations: %+v", view.Citations)
			}
			if view.ID == 0 {
				t.Fatal("the disclosed zero was not persisted")
			}
			if w.prio.topicsCalls != 1 || w.gen.calls != 0 {
				t.Fatalf("census reads=%d generator calls=%d, want 1/0", w.prio.topicsCalls, w.gen.calls)
			}
		})
	}
}

// TestProviderlessTopicCountControls pins the fail-closed boundary: a dated,
// person, organization, action, repeated, compound, unlisted-scope or
// cue-less question keeps the freeform path and never reads the census.
func TestProviderlessTopicCountControls(t *testing.T) {
	questions := []string{
		"How many invoices this week?",
		"How many invoices today?",
		"How many invoices does Bob have?",
		"How many invoices from Atelier Nord?",
		"How many invoices did I approve?",
		"How many invoices Invoice?",
		"How many invoices and billing messages?",
		"How many urgent invoices?",
		"How many budget invoices?",
		"Show me invoices",
	}
	for _, question := range questions {
		t.Run(question, func(t *testing.T) {
			w := seedTopicWorld(t)
			w.svc.SetCompanies(companyBook())

			view, err := w.svc.Ask(t.Context(), question, "en")
			if err != nil {
				t.Fatal(err)
			}
			if view.Mode == ModeStructured || view.Intent != IntentFreeform {
				t.Fatalf("control answered as %q/%q: %+v", view.Mode, view.Intent, view)
			}
			if view.Topic != nil {
				t.Fatalf("control produced a topic answer: %+v", view.Topic)
			}
			if w.prio.topicsCalls != 0 {
				t.Fatalf("control read the census %d times", w.prio.topicsCalls)
			}
			if w.gen.calls != 0 {
				t.Fatalf("control called the generator %d times", w.gen.calls)
			}
		})
	}
}

// TestProviderlessTopicCountDoesNotShadowPriorityIntents pins the precedence:
// the priority category gate is tried before the topic table, so a decision
// or urgent count keeps its structured intent and never reads the census.
func TestProviderlessTopicCountDoesNotShadowPriorityIntents(t *testing.T) {
	cases := []struct {
		question string
		intent   string
	}{
		{"How many decisions?", IntentDecision},
		{"How many urgent messages?", IntentUrgent},
	}
	for _, tc := range cases {
		t.Run(tc.question, func(t *testing.T) {
			w := newWorld(t, false)
			w.svc.source = &fakeSource{err: ErrUnavailable}
			w.prio.byFilter[priority.FilterDecision] = []priority.Item{item(14, "decision")}
			w.prio.byFilter[priority.FilterUrgent] = []priority.Item{item(42, "urgent")}

			view, err := w.svc.Ask(t.Context(), tc.question, "en")
			if err != nil {
				t.Fatal(err)
			}
			if view.Mode != ModeStructured || view.Intent != tc.intent {
				t.Fatalf("mode/intent = %q/%q, want structured/%q", view.Mode, view.Intent, tc.intent)
			}
			if w.prio.topicsCalls != 0 {
				t.Fatalf("a priority count read the topic census %d times", w.prio.topicsCalls)
			}
		})
	}
}

// TestProviderlessTopicCountDefersOnResolverErrors mirrors the structured
// count deferral: a person-store or company-book failure keeps the question
// freeform and reads no census, so an unresolved scope cannot answer a
// mailbox-wide topic total.
func TestProviderlessTopicCountDefersOnResolverErrors(t *testing.T) {
	cases := []struct {
		name        string
		set         func(*testWorld)
		wantCompany bool
		wantPerson  bool
	}{
		{"person store", func(w *testWorld) {
			w.store.peopleErr = errors.New("person store unavailable")
		}, false, true},
		{"company book", func(w *testWorld) {
			w.svc.SetCompanies(&fakeCompanies{listErr: errors.New("company book unavailable")})
		}, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, false)
			logs, err := logx.Setup(w.cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = logs.Close() })

			w.svc.source = &fakeSource{err: ErrUnavailable}
			w.prio.topics = topicTestCensus()
			tc.set(w)

			view, err := w.svc.Ask(t.Context(), "How many invoices?", "en")
			if err != nil {
				t.Fatal(err)
			}
			if view.Mode == ModeStructured || view.Intent != IntentFreeform || view.Topic != nil {
				t.Fatalf("a resolver error answered the topic count: %+v", view)
			}
			if w.prio.topicsCalls != 0 {
				t.Fatalf("a resolver error still read the census %d times", w.prio.topicsCalls)
			}

			var deferred, companyLog, personLog bool
			for _, entry := range logs.Ring().Snapshot() {
				switch entry.Message {
				case "qa providerless count scope deferred":
					deferred = true
				case "qa company book unavailable":
					companyLog = true
				case "qa person candidates unavailable":
					personLog = true
				}
			}
			if !deferred {
				t.Fatal("the strict providerless scope check did not log its deferral")
			}
			if tc.wantCompany && !companyLog {
				t.Fatal("the company-book gate failure was not logged")
			}
			if tc.wantPerson && !personLog {
				t.Fatal("the person-candidate gate failure was not logged")
			}
		})
	}
}

// TestProviderlessTopicCountCensusFailureIsAnError proves a failed census
// read is returned, never turned into a zero: no answer, no history row.
func TestProviderlessTopicCountCensusFailureIsAnError(t *testing.T) {
	w := newWorld(t, false)
	w.svc.source = &fakeSource{err: ErrUnavailable}
	w.prio.topicsErr = errors.New("census unavailable")

	view, err := w.svc.Ask(t.Context(), "How many invoices?", "en")
	if err == nil {
		t.Fatalf("census failure returned an answer: %+v", view)
	}
	if view.ID != 0 || view.Mode != "" || view.Topic != nil {
		t.Fatalf("census failure fabricated an answer: %+v", view)
	}
	if w.prio.topicsCalls != 1 {
		t.Fatalf("census reads = %d, want 1", w.prio.topicsCalls)
	}
	if len(w.store.saved) != 0 {
		t.Fatalf("census failure wrote %d history rows", len(w.store.saved))
	}
	if w.gen.calls != 0 {
		t.Fatalf("census failure called the generator %d times", w.gen.calls)
	}
}

// TestTopicAliasesAvoidPriorityAndScopeWords enforces the table's boundary
// from the Q&A side: no alias run may contain a priority-intent word or a
// pinned scope word, and no standalone generic word may be an alias.
func TestTopicAliasesAvoidPriorityAndScopeWords(t *testing.T) {
	priorityWords := map[string]bool{}
	for _, words := range intentWords {
		for _, word := range words {
			priorityWords[word] = true
		}
	}
	scoped := []string{"budget", "clients", "upcoming", "active", "left", "quarter", "week", "semaine"}
	generic := []string{"test", "request", "information", "question"}

	rows := subject.Phrases()
	if len(rows) == 0 {
		t.Fatal("the phrase table is empty")
	}
	for _, row := range rows {
		for _, alias := range append(append([]string(nil), row.EN...), row.FR...) {
			words := wordList(alias)
			if len(words) == 0 {
				t.Errorf("%s/%s: alias %q has no words", row.Topic, row.Subtopic, alias)
				continue
			}
			if len(words) == 1 {
				for _, word := range generic {
					if words[0] == word {
						t.Errorf("alias %q is a standalone generic word", alias)
					}
				}
			}
			for _, word := range words {
				if priorityWords[word] {
					t.Errorf("alias %q uses the priority word %q", alias, word)
				}
				for _, scope := range scoped {
					if word == scope {
						t.Errorf("alias %q uses the pinned scope word %q", alias, word)
					}
				}
			}
		}
	}
}

// TestAskLocalAnswersATopicCountWithoutAGenerator pins the Local route arm:
// an explicit local count of a topic is answered from the census without any
// generator call and stays on RouteLocal.
func TestAskLocalAnswersATopicCountWithoutAGenerator(t *testing.T) {
	w := newWorld(t, false)
	w.prio.topics = topicTestCensus()
	w.store.summaries[9] = store.Summary{RowID: 9, Subject: "subject", SenderName: "sender",
		SenderAddress: "sender@example.com", Instant: "2026-10-25T10:00:00Z"}

	view, err := w.svc.AskConversation(t.Context(), AskRequest{
		Question: "How many messages about invoices?", Language: "en", Route: RouteLocal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.Route != RouteLocal || view.Mode != ModeStructured || view.Intent != IntentTopic {
		t.Fatalf("view = route %q mode %q intent %q; want local/structured/topic", view.Route, view.Mode, view.Intent)
	}
	if view.Topic == nil || view.Topic.Topic != "billing_account" || view.Topic.Subtopic != "invoice" ||
		view.Topic.Count != 2 {
		t.Fatalf("topic = %+v", view.Topic)
	}
	if w.gen.calls != 0 {
		t.Fatalf("Local called the generator %d times", w.gen.calls)
	}
	if w.prio.topicsCalls != 1 {
		t.Fatalf("census reads = %d, want 1", w.prio.topicsCalls)
	}
	if got := w.store.saved[0].Route; got != string(RouteLocal) {
		t.Fatalf("stored route = %q", got)
	}
	if w.store.saved[0].Topic == nil || w.store.saved[0].Topic.Count != 2 {
		t.Fatalf("stored topic = %+v", w.store.saved[0].Topic)
	}
}
