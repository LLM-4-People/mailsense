package qa

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/search"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// historyItems reads the rail rows and then every detail: the two reads the
// UI performs, in the order it performs them.
func historyItems(t *testing.T, w *testWorld, limit int) []AskView {
	t.Helper()
	rows, err := w.svc.History(t.Context(), limit)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]AskView, len(rows))
	for i, row := range rows {
		view, ok, err := w.svc.HistoryItem(t.Context(), row.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Fatalf("history row %d vanished", row.ID)
		}
		out[i] = view
	}
	return out
}

// The history rail is a four-field projection: a list mount never rehydrates
// citations or decodes a plan, and opening a row is the one full read.
func TestHistoryListIsAProjection(t *testing.T) {
	w := newWorld(t, false)
	w.store.saved = []store.QAHistory{{ID: 2, Question: "q2", Answer: "a2", Mode: ModeGenerated,
		CreatedAt: "2026-10-26T12:00:00Z"}}
	rows, err := w.svc.History(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != 2 || rows[0].Question != "q2" ||
		rows[0].Mode != ModeGenerated || rows[0].CreatedAt == "" {
		t.Fatalf("history rows %+v", rows)
	}
	if w.store.summariesCalls != 0 {
		t.Fatalf("the history list rehydrated citations: %d summaries calls", w.store.summariesCalls)
	}
	if _, ok, err := w.svc.HistoryItem(t.Context(), 2); err != nil || !ok {
		t.Fatalf("history detail: ok=%v err=%v", ok, err)
	}
	if w.store.summariesCalls != 1 {
		t.Fatalf("history detail summaries calls = %d, want 1", w.store.summariesCalls)
	}
}

// TestHistoryRoundTripsFallbackNotices proves every new fallback code is
// stored with its answer and read back unchanged, including the provider and
// model of a failed generation.
func TestHistoryRoundTripsFallbackNotices(t *testing.T) {
	w := newWorld(t, false)
	ask := func(question string) AskView {
		t.Helper()
		view, err := w.svc.Ask(t.Context(), question, "en")
		if err != nil {
			t.Fatal(err)
		}
		return view
	}

	w.search.result = search.Result{Hits: []search.Hit{hit(1)}}
	w.store.summaries[1] = store.Summary{RowID: 1, Subject: "s", Body: "body"}
	w.source.gen, w.source.err = nil, fmt.Errorf("%w: no key", ErrUnavailable)
	ask("Question without a configured provider")
	w.source.gen, w.source.err = w.gen, nil
	w.gen.err = context.DeadlineExceeded
	ask("Question whose provider call times out")
	w.gen.err = nil
	w.gen.content = `{"route":"answer","answer":"claim","used":[9]}`
	ask("Question whose answer has no citation")
	w.gen.content = `{"route":"answer","answer":"Grounded [1].","used":[1]}`
	ask("What did Alice write about the laptop?")

	views := historyItems(t, w, 10)
	want := []struct {
		notice   string
		provider string
		model    string
	}{
		{"", "fake", "fake-model"},
		{NoticeNoGroundedAnswer, "fake", "fake-model"},
		{NoticeGenerationFailed, "fake", "fake-model"},
		{NoticeUnavailable, "", ""},
	}
	if len(views) != len(want) {
		t.Fatalf("history has %d rows, want %d: %+v", len(views), len(want), views)
	}
	for i, want := range want {
		if views[i].Notice != want.notice || views[i].Provider != want.provider || views[i].Model != want.model {
			t.Errorf("history row %d notice/provider/model = %q/%q/%q, want %q/%q/%q",
				i, views[i].Notice, views[i].Provider, views[i].Model, want.notice, want.provider, want.model)
		}
	}
}

// TestHistoryNormalizesLegacyUnavailableRows proves rows written before the
// notice split no longer tell a configured owner to set up a provider.
func TestHistoryNormalizesLegacyUnavailableRows(t *testing.T) {
	w := newWorld(t, false)
	w.store.saved = []store.QAHistory{
		{ID: 1, Question: "legacy provider failure", Mode: ModeRetrieval, Notice: NoticeUnavailable,
			Provider: "p", Model: "m", Citations: []store.QACitation{{MessageID: 1}}},
		{ID: 2, Question: "legacy missing provider", Mode: ModeRetrieval, Notice: NoticeUnavailable,
			Citations: []store.QACitation{{MessageID: 2}}},
		{ID: 3, Question: "legacy warming index", Mode: ModeRetrieval, Notice: NoticeUnavailable},
	}
	views := historyItems(t, w, 10)
	if len(views) != 3 {
		t.Fatalf("history %+v", views)
	}
	if views[0].Notice != NoticeRetrievalTimeout {
		t.Errorf("legacy no-provider/no-citation row = %q, want %q", views[0].Notice, NoticeRetrievalTimeout)
	}
	if views[1].Notice != NoticeUnavailable {
		t.Errorf("legacy no-provider/cited row = %q, want %q", views[1].Notice, NoticeUnavailable)
	}
	if views[2].Notice != NoticeGenerationFailed {
		t.Errorf("legacy provider row = %q, want %q", views[2].Notice, NoticeGenerationFailed)
	}
}

func TestRetrievalWithNoSourcesAndRawJSONFallback(t *testing.T) {
	w := newWorld(t, false)
	w.gen.content = `{"route":"answer","answer":"I cannot answer from the mailbox.","used":[]}`
	view, err := w.svc.Ask(t.Context(), "A question with no matching mail", "en")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeRetrieval || view.Notice != NoticeNoMatches || len(view.Citations) != 0 {
		t.Fatalf("no-source retrieval: %+v", view)
	}
	if w.source.calls != 1 || w.gen.calls != 1 {
		t.Fatalf("the planner must decide, not be skipped: source=%d generator=%d", w.source.calls, w.gen.calls)
	}
	w.search.result = search.Result{Hits: []search.Hit{hit(1)}}
	w.store.summaries[1] = store.Summary{RowID: 1, Subject: "s", Body: "body"}
	w.gen.content = "A plain answer [1]"
	view, err = w.svc.Ask(t.Context(), "Tell me something", "en")
	if err != nil {
		t.Fatal(err)
	}
	if view.Mode != ModeGenerated || view.Answer != "A plain answer [1]" || len(view.Citations) != 1 {
		t.Fatalf("raw fallback %+v", view)
	}
}

func TestHistoryNormalizesPreNoticeRows(t *testing.T) {
	w := newWorld(t, false)
	w.store.saved = []store.QAHistory{
		{ID: 1, Question: "pre-notice provider failure", Mode: ModeRetrieval,
			Provider: "p", Model: "m", Citations: []store.QACitation{{MessageID: 1}}},
		{ID: 2, Question: "pre-notice missing provider", Mode: ModeRetrieval,
			Citations: []store.QACitation{{MessageID: 2}}},
		{ID: 3, Question: "pre-notice no match", Mode: ModeRetrieval},
	}
	views := historyItems(t, w, 10)
	if len(views) != 3 {
		t.Fatalf("history %+v", views)
	}
	want := []string{NoticeNoMatches, NoticeUnavailable, NoticeGenerationFailed}
	for i, notice := range want {
		if views[i].Notice != notice {
			t.Errorf("history row %d notice = %q, want %q", i, views[i].Notice, notice)
		}
	}
}

func TestHistoryRehydrationMissingAndClear(t *testing.T) {
	w := newWorld(t, false)
	w.store.saved = []store.QAHistory{
		{ID: 2, Question: "q2", Answer: "a2", Mode: ModeGenerated, Intent: IntentFreeform, Language: "en",
			Counts: map[string]int{"total": 2}, Citations: []store.QACitation{{MessageID: 7, Reason: "reply"}, {MessageID: 8, Reason: "urgent"}},
			Provider: "p", Model: "m", Preview: true, CreatedAt: "2026-10-26T12:00:00Z"},
		{ID: 1, Question: "q1", Answer: "", Mode: ModeRetrieval, Intent: IntentFreeform, Language: "fr",
			Counts: map[string]int{}, Citations: []store.QACitation{{MessageID: 9}}, CreatedAt: "2026-10-25T12:00:00Z"},
	}
	w.store.summaries[7] = store.Summary{RowID: 7, Subject: "subject seven", SenderName: "Alice", Instant: "2026-10-25T10:00:00Z"}
	views := historyItems(t, w, 10)
	if len(views) != 2 || views[0].ID != 2 || len(views[0].Citations) != 2 {
		t.Fatalf("history %+v", views)
	}
	if views[0].Citations[0].Subject != "subject seven" || views[0].Citations[0].SenderName != "Alice" {
		t.Fatalf("rehydrated citation %+v", views[0].Citations[0])
	}
	if views[0].Citations[1].Subject != "" || views[0].Citations[1].MessageID != 8 {
		t.Fatalf("missing message must keep its id: %+v", views[0].Citations[1])
	}
	if views[1].Notice != NoticeUnavailable {
		t.Fatalf("retrieval history notice %+v", views[1])
	}
	if err := w.svc.ClearHistory(t.Context()); err != nil || w.store.clearCalls != 1 {
		t.Fatalf("clear: %v calls %d", err, w.store.clearCalls)
	}
	if _, err := w.svc.History(t.Context(), 0); err == nil {
		t.Fatal("history limit 0 accepted")
	}
	w.gate.err = errors.New("locked")
	if _, err := w.svc.History(t.Context(), 1); err == nil {
		t.Fatal("history bypassed the gate")
	}
	if err := w.svc.ClearHistory(t.Context()); err == nil {
		t.Fatal("clear bypassed the gate")
	}
}
