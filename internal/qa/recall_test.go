package qa

// The offline known-item recall harness. Synthetic messages are stored in a
// real store and retrieved through the production search path with the
// semantic leg empty, so a failure here is a full-text retrieval failure, not
// a fake-collaborator artefact. Only aggregates are logged: no subject, body
// or address is printed.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/learning/learningtest"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/priority"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/search"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
)

type recallPriority struct{}

func (recallPriority) Rank(context.Context, priority.Filter, int) ([]priority.Item, int, bool, error) {
	return nil, 0, false, nil
}

func (recallPriority) Top(context.Context, int) ([]priority.Item, error) { return nil, nil }

func (recallPriority) ThreadStates(context.Context) (map[string]threadintel.Assessment, error) {
	return nil, nil
}

type recallCoverage struct{}

func (recallCoverage) Eligible(context.Context) ([]int64, error) { return nil, nil }

type recallEmbedder struct{}

func (recallEmbedder) Name() string               { return "none" }
func (recallEmbedder) Model() string              { return "none" }
func (recallEmbedder) Space() string              { return "none" }
func (recallEmbedder) Dimensions() int            { return 8 }
func (recallEmbedder) Input(t engine.Text) string { return t.Body }
func (recallEmbedder) Embed(context.Context, []engine.Text) (engine.Embeddings, error) {
	return engine.Embeddings{}, errors.New("recall harness: no embedder")
}

type recallEmbedderSource struct{}

func (recallEmbedderSource) Embedder() (engine.Embedder, error) { return recallEmbedder{}, nil }

type recallFixture struct {
	svc  *Service
	st   *store.Store
	srch *search.Searcher
	ids  map[string]int64
}

// openRecallFixture seeds a mailbox with five answer-bearing messages and
// twenty generic distractors. The store, scope and search are the production
// implementations; only the model is absent.
func openRecallFixture(t *testing.T) *recallFixture {
	t.Helper()
	ctx := t.Context()
	cfg := configtest.New(t, map[string]any{
		source.SettingSelected: mailtest.Picked([]string{"src"}),
		"qa.max_messages":      8,
		"qa.retrieval_timeout": "10s",
	})
	st, err := store.Open(ctx, cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.UpsertSource(ctx, "src", "test", "Test", source.Capabilities{}); err != nil {
		t.Fatal(err)
	}

	type seed struct {
		id, subject, body string
	}
	targets := []seed{
		{"m1", "Budget approval", "The approval code is ZX-42. Alice approved the invoice on Friday."},
		{"m2", "Invoice payment", "The payment is due on 15 November. Please confirm the transfer."},
		{"m3", "Server migration", "The server migration window is Saturday at 02:00 UTC. No downtime is expected."},
		{"m4", "Hiring plan", "The hiring plan adds three engineers next quarter. Dave will own the interviews."},
		{"m5", "Lease renewal", "The lease renewal is waiting for the landlord's signature. The landlord is Erin."},
	}
	seeds := append([]seed{}, targets...)
	for i := 0; i < 20; i++ {
		seeds = append(seeds, seed{fmt.Sprintf("d%02d", i), fmt.Sprintf("Weekly update %d", i),
			"The weekly update is attached. Please review the notes before the meeting."})
	}
	base := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	msgs := make([]mail.Message, 0, len(seeds))
	for i, s := range seeds {
		m := mailtest.Message("src", s.id)
		m.Subject, m.Body = s.subject, s.body
		m.ConversationID = "C-" + s.id
		m.HasAttachments, m.TaskDueDate = false, ""
		m.ReceivedAt = base.Add(time.Duration(i) * time.Minute).Format(mail.TimeLayout)
		m.SentOn = ""
		msgs = append(msgs, m)
	}
	if err := st.UpsertMessages(ctx, 0, msgs); err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	var verdicts []store.VerdictRow
	if err := st.EachMessage(ctx, store.MessageFilter{}, func(row *store.MessageRow) error {
		ids[row.Message.ID] = row.RowID
		verdicts = append(verdicts, store.VerdictRow{RowID: row.RowID, Verdict: scope.Owner, Reason: scope.ReasonRecipientMatch})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteVerdicts(ctx, "recall", verdicts); err != nil {
		t.Fatal(err)
	}
	srch, err := search.New(cfg, search.Deps{Store: st, Gate: learningtest.Open{},
		Coverage: recallCoverage{}, Embedder: recallEmbedderSource{}})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(cfg, Deps{Store: st, Priority: recallPriority{}, Search: srch, Gate: learningtest.Open{}})
	if err != nil {
		t.Fatal(err)
	}
	return &recallFixture{svc: svc, st: st, srch: srch, ids: ids}
}

func TestKnownItemRetrievalRecall(t *testing.T) {
	f := openRecallFixture(t)
	questions := []struct{ question, target string }{
		{"What is the ZX-42 approval code?", "m1"},
		{"When is the payment due?", "m2"},
		{"When is the server migration window?", "m3"},
		{"What did Dave say about the hiring plan?", "m4"},
		{"Who is waiting for the lease renewal signature?", "m5"},
		{"ZX-42 approval", "m1"},
		{"payment due", "m2"},
		{"server migration window", "m3"},
	}

	var strict, recall int
	for _, tc := range questions {
		// The strict AND query is the pre-fallback full-text path.
		strictRes, err := f.st.SearchText(t.Context(), tc.question,
			store.SearchFilter{OwnerOnly: true}, 200)
		if err != nil {
			t.Fatal(err)
		}
		for _, hit := range strictRes.Hits {
			if hit.RowID == f.ids[tc.target] {
				strict++
				break
			}
		}

		msgs, _, _, err := f.svc.plannerContext(t.Context(), tc.question, nil)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, msg := range msgs {
			if msg.ID == f.ids[tc.target] {
				found = true
				break
			}
		}
		if found {
			recall++
		}
		if len(msgs) > 8 {
			t.Fatalf("question %q returned %d messages, over qa.max_messages", tc.question, len(msgs))
		}
	}
	t.Logf("known_item questions=%d strict_recall=%d/%d fallback_recall=%d/%d",
		len(questions), strict, len(questions), recall, len(questions))
	if strict >= recall {
		t.Fatalf("the recall fallback did not improve retrieval (strict %d, fallback %d)", strict, recall)
	}
	if recall != len(questions) {
		t.Fatalf("known-item recall regressed: %d/%d", recall, len(questions))
	}
}

func TestInteractiveSearchKeepsTheStrictMatch(t *testing.T) {
	f := openRecallFixture(t)
	res, err := f.srch.Search(t.Context(), search.Query{Text: "when is the payment due?",
		Filter: store.SearchFilter{OwnerOnly: true}, Limit: 8})
	if err != nil {
		t.Fatal(err)
	}
	if res.Recall {
		t.Fatalf("an interactive search used the question-answering recall fallback")
	}
	if len(res.Hits) != 0 {
		t.Fatalf("the strict interactive search matched %d messages; the fallback boundary moved", len(res.Hits))
	}

	waiting, err := f.srch.Search(t.Context(), search.Query{Text: "when is the payment due?",
		Filter: store.SearchFilter{OwnerOnly: true}, Limit: 8, Wait: true})
	if err != nil {
		t.Fatal(err)
	}
	if !waiting.Recall || len(waiting.Hits) == 0 {
		t.Fatalf("a waiting question did not use the recall fallback: recall=%v hits=%d", waiting.Recall, len(waiting.Hits))
	}
}
