package store

import (
	"fmt"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
)

func TestQAHistoryRoundTripPruneAndClear(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	for i := range QAHistoryMax + 5 {
		saved, err := s.SaveQA(ctx, QAHistory{
			Question: fmt.Sprintf("question %d", i), Answer: fmt.Sprintf("answer %d", i), Mode: "structured",
			Intent: "reply", Counts: map[string]int{"reply": i}, Citations: []QACitation{{MessageID: 1, Reason: "reply"}},
			Provider: "p", Model: "m", Language: "fr", Preview: i%2 == 0,
		})
		if err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
		if saved.ID <= 0 || saved.CreatedAt == "" {
			t.Fatalf("saved row %d: %+v", i, saved)
		}
	}
	rows, err := s.ListQA(ctx, QAHistoryMax)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != QAHistoryMax {
		t.Fatalf("kept %d rows, want %d", len(rows), QAHistoryMax)
	}
	if rows[0].Question != "question 104" || rows[len(rows)-1].Question != "question 5" {
		t.Fatalf("newest 100 not kept: first %q last %q", rows[0].Question, rows[len(rows)-1].Question)
	}
	if rows[0].Counts["reply"] != 104 || len(rows[0].Citations) != 1 || rows[0].Citations[0].MessageID != 1 ||
		!rows[0].Preview || rows[0].Provider != "p" || rows[0].Model != "m" || rows[0].Language != "fr" {
		t.Fatalf("round trip %+v", rows[0])
	}
	if rows[1].Preview {
		t.Fatal("the preview flag did not round trip")
	}
	if _, err := s.ListQA(ctx, 0); err == nil {
		t.Fatal("history limit 0 accepted")
	}
	if _, err := s.SaveQA(ctx, QAHistory{Mode: "", Intent: "x", Language: "fr"}); err == nil {
		t.Fatal("an empty mode was accepted")
	}
	if _, err := s.SaveQA(ctx, QAHistory{Mode: "structured", Intent: "x", Language: "fr",
		Citations: []QACitation{{MessageID: 0}}}); err == nil {
		t.Fatal("a zero citation id was accepted")
	}
	n, err := s.ClearQA(ctx)
	if err != nil || n != QAHistoryMax {
		t.Fatalf("clear: %d %v", n, err)
	}
	if rows, err := s.ListQA(ctx, 10); err != nil || len(rows) != 0 {
		t.Fatalf("after clear: %d %v", len(rows), err)
	}
}

// TestQAHistoryTopicAnswerRoundTrip proves the topic count answer survives a
// real SQLite write and read, decodes strictly, and is absent (never a
// fabricated zero) in a row written before the column existed.
func TestQAHistoryTopicAnswerRoundTrip(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	want := QATopicAnswer{Topic: "billing_account", Subtopic: "invoice", Count: 3, Classified: 40, Scanned: 52}
	saved, err := s.SaveQA(ctx, QAHistory{
		Question: "Combien de factures ?", Mode: "structured", Intent: "topic", Language: "fr",
		Counts: map[string]int{}, Citations: []QACitation{}, Topic: &want,
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListQA(ctx, 10)
	if err != nil || len(rows) != 1 || rows[0].ID != saved.ID {
		t.Fatalf("list: %d rows, %v", len(rows), err)
	}
	got := rows[0].Topic
	if got == nil || *got != want {
		t.Fatalf("topic round trip = %+v, want %+v", got, want)
	}
	if rows[0].Mode != "structured" || rows[0].Intent != "topic" {
		t.Fatalf("row = %+v", rows[0])
	}

	legacy, err := s.SaveQA(ctx, QAHistory{Question: "How many urgent messages?",
		Mode: "structured", Intent: "urgent", Language: "en", Counts: map[string]int{}})
	if err != nil {
		t.Fatal(err)
	}
	rows, err = s.ListQA(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == legacy.ID && row.Topic != nil {
			t.Fatalf("legacy row fabricated a topic answer: %+v", row.Topic)
		}
	}

	if _, err := s.SaveQA(ctx, QAHistory{Mode: "structured", Intent: "topic", Language: "fr",
		Topic: &QATopicAnswer{Topic: "", Count: 1}}); err == nil {
		t.Fatal("a topic answer without a topic was accepted")
	}
	if _, err := s.SaveQA(ctx, QAHistory{Mode: "structured", Intent: "topic", Language: "fr",
		Topic: &QATopicAnswer{Topic: "billing_account", Count: -1}}); err == nil {
		t.Fatal("a negative topic count was accepted")
	}

	if _, err := s.db.ExecContext(ctx, `INSERT INTO qa_history
		(question, answer, mode, intent, counts, citations, language, preview, created_at, topic)
		VALUES ('q', 'a', 'structured', 'topic', '{}', '[]', 'fr', 0, 'x', '{')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListQA(ctx, 10); err == nil {
		t.Fatal("malformed topic JSON must fail the read")
	}
}

// TestQAHistoryPersistsAnswerLanguageNotice proves the language notice survives
// a real SQLite write and read. internal/store cannot import
// mailsense/internal/qa: qa imports store, so the internal test package would
// close an import cycle. The literal is therefore the wire value the Q&A
// service writes, and the qa package pins the same value in its API test.
func TestQAHistoryPersistsAnswerLanguageNotice(t *testing.T) {
	const answerLanguageNotice = "answer_language"
	s, _ := open(t, nil)
	ctx := t.Context()
	want := QAHistory{
		ConversationID: "conv-notice",
		Question:       "Que s'est-il passé avec la facture ?",
		Answer:         "The invoice was approved by the finance team and will be paid this week [1].",
		Mode:           "generated",
		Intent:         "freeform",
		Counts:         map[string]int{"total": 1},
		Citations:      []QACitation{{MessageID: 7, Reason: "retrieved"}},
		Provider:       "fake",
		Model:          "fake-model",
		Language:       "fr",
		Notice:         answerLanguageNotice,
	}
	saved, err := s.SaveQA(ctx, want)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID <= 0 || saved.Notice != answerLanguageNotice {
		t.Fatalf("saved row: %+v", saved)
	}

	rows, err := s.ListQA(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	var listed *QAHistory
	for i := range rows {
		if rows[i].ID == saved.ID {
			listed = &rows[i]
			break
		}
	}
	if listed == nil {
		t.Fatalf("saved row %d is not listed: %+v", saved.ID, rows)
	}
	if listed.Notice != answerLanguageNotice || listed.Question != want.Question ||
		listed.Answer != want.Answer || listed.Language != want.Language || listed.Mode != want.Mode {
		t.Fatalf("listed row lost persisted fields: %+v", listed)
	}

	turns, err := s.ListQATurns(ctx, "conv-notice", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0].ID != saved.ID || turns[0].Notice != answerLanguageNotice {
		t.Fatalf("conversation read lost the notice: %+v", turns)
	}
}

func TestRepairSavedTextOnce(t *testing.T) {
	// One connection proves the read is closed before the repair transaction:
	// an open cursor would hold the only connection and deadlock startup.
	s, _ := open(t, map[string]any{"store.max_open_conns": 1})
	ctx := t.Context()
	saved, err := s.SaveQA(ctx, QAHistory{
		Question: "RÃ©sume le dernier Ã©change avec messagerie-exemple.test",
		Answer:   "lâ€™infrastructure est prÃªte", Mode: "generated", Intent: "freeform",
		Counts: map[string]int{"total": 1}, Language: "fr",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM meta WHERE key = ?`, textRepairKey); err != nil {
		t.Fatal(err)
	}
	if err := s.repairSavedText(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListQA(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != saved.ID {
		t.Fatalf("rows %+v", rows)
	}
	if rows[0].Question != "Résume le dernier échange avec messagerie-exemple.test" {
		t.Fatalf("question = %q", rows[0].Question)
	}
	if rows[0].Answer != "l’infrastructure est prête" {
		t.Fatalf("answer = %q", rows[0].Answer)
	}
	// The repair is recorded: a second open must not rewrite the table.
	var marker string
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, textRepairKey).Scan(&marker); err != nil || marker != "1" {
		t.Fatalf("repair marker = %q, %v", marker, err)
	}
	if err := s.repairSavedText(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestQAHistoryMalformedJSONFails(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO qa_history
		(question, answer, mode, intent, counts, citations, language, preview, created_at)
		VALUES ('q', 'a', 'structured', 'reply', '{', '[]', 'fr', 0, 'x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListQA(ctx, 10); err == nil {
		t.Fatal("malformed counts must fail the read")
	}
}

func TestQAConversationTurnsScopedAndOldestFirst(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	save := func(conversationID, question string) QAHistory {
		t.Helper()
		rec, err := s.SaveQA(ctx, QAHistory{
			ConversationID: conversationID, Question: question, Answer: "answer " + question,
			Mode: "generated", Intent: "freeform", Counts: map[string]int{"total": 1},
			Citations: []QACitation{{MessageID: 1, Reason: "retrieved"}}, Language: "fr",
		})
		if err != nil {
			t.Fatalf("save %q: %v", question, err)
		}
		return rec
	}
	first := save("  conv-a  ", "first")
	if first.ConversationID != "conv-a" {
		t.Fatalf("conversation id was not normalized: %q", first.ConversationID)
	}
	other := save("conv-b", "other")
	second := save("conv-a", "second")
	third := save("conv-a", "third")

	turns, err := s.ListQATurns(ctx, "conv-a", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 || turns[0].ID != second.ID || turns[1].ID != third.ID {
		t.Fatalf("newest two turns are not oldest first: %+v", turns)
	}
	if turns[0].ConversationID != "conv-a" || turns[0].Counts["total"] != 1 ||
		len(turns[0].Citations) != 1 || turns[0].Citations[0].MessageID != 1 {
		t.Fatalf("turn was not rehydrated: %+v", turns[0])
	}

	all, err := s.ListQATurns(ctx, "conv-a", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].ID != first.ID || all[1].ID != second.ID || all[2].ID != third.ID {
		t.Fatalf("conversation turns are not oldest first: %+v", all)
	}
	for _, turn := range all {
		if turn.ID == other.ID {
			t.Fatal("another conversation's turn was returned")
		}
	}

	rows, err := s.ListQA(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := map[int64]string{}
	for _, row := range rows {
		found[row.ID] = row.ConversationID
	}
	if found[first.ID] != "conv-a" || found[other.ID] != "conv-b" {
		t.Fatalf("ListQA lost the conversation ids: %+v", found)
	}

	for _, limit := range []int{0, QATurnsMax + 1} {
		if _, err := s.ListQATurns(ctx, "conv-a", limit); err == nil {
			t.Fatalf("conversation limit %d was accepted", limit)
		}
	}
	for _, id := range []string{"", "bad id", "conv.a", strings.Repeat("a", QAConversationIDMax+1)} {
		if _, err := s.ListQATurns(ctx, id, 2); err == nil {
			t.Fatalf("conversation id %q was accepted", id)
		}
	}
	if _, err := s.SaveQA(ctx, QAHistory{ConversationID: "bad id", Question: "q", Mode: "generated",
		Intent: "freeform", Language: "fr"}); err == nil {
		t.Fatal("SaveQA accepted an invalid conversation id")
	}
}

func TestSummariesOwnerExcludesSharedAndMissing(t *testing.T) {
	s, _ := open(t, nil)
	owner := answerMessage(t, s, mailtest.Message("src", "owner"))
	shared := answerMessage(t, s, mailtest.Message("src", "shared"))
	answerVerdict(t, s, owner, scope.Owner)
	answerVerdict(t, s, shared, scope.Shared)
	sums, err := s.SummariesOwner(t.Context(), []int64{owner, shared, 999999})
	if err != nil {
		t.Fatal(err)
	}
	if len(sums) != 1 || sums[owner].RowID != owner {
		t.Fatalf("owner summaries %+v", sums)
	}
	all, err := s.Summaries(t.Context(), []int64{owner, shared})
	if err != nil || len(all) != 2 {
		t.Fatalf("default summaries must stay scope-free: %d %v", len(all), err)
	}
}
