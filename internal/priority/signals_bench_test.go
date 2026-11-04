package priority

import (
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/route"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// benchRow is one prepared fixture message: its typed scoring input, the
// rowSignals the reply pass derives once, and the stored timestamp string the
// pipeline parses.
type benchRow struct {
	in      Input
	rs      rowSignals
	route   map[string]route.Answer
	instant string
	at      time.Time
	dated   bool
}

// benchRows builds a fixture that exercises every branch the row scoring
// reads: a reply or a question, an ask, a deadline, a threat, bulk categories,
// an automated service and an old row. It is deliberately mixed so the
// benchmark is not one hot branch.
func benchRows(b *testing.B, n int) ([]benchRow, *taxonomy.Profile, []store.QuestionKey) {
	b.Helper()
	p, err := taxonomy.Default()
	if err != nil {
		b.Fatal(err)
	}
	keys := neededQuestions(p)
	categories := []string{"work", "action", "finance", "security", "notifications", "marketing", "spam"}
	topics := []string{"project_work", "billing_account", "support_issue", "meeting_scheduling", "news_updates"}
	asks := []string{"answer_question", "payment_money", "task_work", "information_document", "nothing_fyi"}
	rows := make([]benchRow, n)
	for i := range rows {
		routeAnswers := map[string]route.Answer{
			"reply_needed":    noul("reply_needed", 0.2),
			"is_question":     noul("is_question", 0.9),
			"decision_needed": noul("decision_needed", 0.1),
			"suspicious":      noul("suspicious", 0.1),
			"call_to_action":  noul("call_to_action", 0.1),
			"category":        choiceAns("category", categories[i%len(categories)]),
			"topic":           choiceAns("topic", topics[i%len(topics)]),
			"asks_for":        choiceAns("asks_for", asks[i%len(asks)]),
			"relationship":    choiceAns("relationship", "existing_client"),
			"urgency":         score("urgency", float64(i%5)),
			"deadline":        score("deadline", float64(i%6)),
			"effort":          score("effort", float64(i%4)),
			"request_count":   score("request_count", float64(i%4)),
		}
		if i%3 == 0 {
			routeAnswers["reply_needed"] = noul("reply_needed", 0.9)
		}
		if i%4 == 0 {
			routeAnswers["decision_needed"] = noul("decision_needed", 0.9)
		}
		if i%16 == 0 {
			routeAnswers["suspicious"] = noul("suspicious", 0.9)
		}
		if i%10 == 0 {
			routeAnswers["relationship"] = choiceAns("relationship", "automated_service")
		}
		received := receivedAgo(time.Duration(i) * time.Hour)
		instant, dated := mail.Parse(received)
		rs, err := signalsFor(p, routeAnswers, nil)
		if err != nil {
			b.Fatal(err)
		}
		rows[i] = benchRow{
			in: Input{Message: Message{
				MessageID: int64(i + 1), ConversationID: "conversation", Subject: "subject",
				SenderName: "sender", SenderAddress: "sender@example.com", ReceivedAt: received,
				FolderRole: mail.RoleInbox, Revision: 1,
			}, Now: testNow},
			rs:      rs,
			route:   routeAnswers,
			instant: received,
			at:      instant,
			dated:   dated,
		}
	}
	return rows, p, keys
}

var benchSink int

// BenchmarkScoreRowSignals measures the scoring pass of one row from the
// rowSignals the reply pass already derived. The fixture is prepared outside
// the timer, so the number is the per-row scoring cost the build pays after
// its one scan.
func BenchmarkScoreRowSignals(b *testing.B) {
	rows, _, _ := benchRows(b, 64)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		row := rows[i%len(rows)]
		_, ok := scoreRowSignals(row.in, row.rs, row.at, row.dated, false)
		if ok {
			benchSink++
		}
	}
}

// BenchmarkPriorityRow measures one build row end to end: derive the row's
// signals from its stored answers, then score it. The build reuses one answer
// map and one parsed instant across rows.
func BenchmarkPriorityRow(b *testing.B) {
	rows, p, keys := benchRows(b, 64)
	answers := make(map[string]engine.Answer, len(keys))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		row := rows[i%len(rows)]
		at, dated := mail.Parse(row.instant)
		rs, err := signalsForKeysInto(p, keys, row.route, nil, answers)
		if err != nil {
			b.Fatal(err)
		}
		_, ok := scoreRowSignals(row.in, rs, at, dated, false)
		if ok {
			benchSink++
		}
	}
}
