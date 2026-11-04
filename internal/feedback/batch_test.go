package feedback

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// batchWorld is a store with n owner messages of one picked source and a
// labeler with a change subscription.
func batchWorld(t *testing.T, n int) (*store.Store, *taxonomy.Profile, []int64, *events.Bus, *Labeler) {
	t.Helper()
	cfg := configtest.New(t, map[string]any{source.SettingSelected: mailtest.Picked([]string{"src"})})
	st, err := store.Open(t.Context(), cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.UpsertSource(t.Context(), "src", "test", "T", source.Capabilities{}); err != nil {
		t.Fatal(err)
	}
	msgs := make([]mail.Message, n)
	for i := range n {
		msgs[i] = mailtest.Message("src", fmt.Sprintf("m%d", i))
	}
	if err := st.UpsertMessages(t.Context(), 0, msgs); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	if err := st.EachMessage(t.Context(), store.MessageFilter{}, func(r *store.MessageRow) error {
		ids = append(ids, r.RowID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	bus := events.New()
	t.Cleanup(bus.Close)
	return st, profile(t), ids, bus, NewLabeler(st, bus)
}

func TestPutBatchStoresAllOrNothing(t *testing.T) {
	st, p, ids, bus, lb := batchWorld(t, 3)
	changes, err := events.Subscribe(bus, TopicLabel, 8)
	if err != nil {
		t.Fatal(err)
	}
	items := []PutItem{
		{MessageID: ids[0], Question: "category", Value: json.RawMessage(`"work"`)},
		{MessageID: ids[1], Question: "reply_needed", Value: json.RawMessage(`true`)},
	}
	got, err := lb.PutBatch(t.Context(), p, items)
	if err != nil || len(got.Stored) != 2 || got.Stored[0].Source != store.LabelManual || got.Stored[1].Source != store.LabelManual {
		t.Fatalf("batch %+v %v", got, err)
	}
	rows, err := st.ListLabels(t.Context())
	if err != nil || len(rows) != 2 {
		t.Fatalf("stored labels %+v %v", rows, err)
	}
	change := <-changes.C
	if change.Action != "set" || change.Count != 2 || change.MessageID != 0 || change.Question != "" {
		t.Fatalf("a batch publishes one event: %+v", change)
	}
	select {
	case extra := <-changes.C:
		t.Fatalf("a batch published another event: %+v", extra)
	default:
	}
	// A bulk answer of one question over several messages names the
	// question but no single message.
	if _, err := lb.PutBatch(t.Context(), p, []PutItem{
		{MessageID: ids[1], Question: "category", Value: json.RawMessage(`"work"`)},
		{MessageID: ids[2], Question: "category", Value: json.RawMessage(`"spam"`)},
	}); err != nil {
		t.Fatal(err)
	}
	change = <-changes.C
	if change.Action != "set" || change.Count != 2 || change.MessageID != 0 || change.Question != "category" {
		t.Fatalf("a one-question batch: %+v", change)
	}
}

func TestPutBatchRefusesEveryProblemWithoutWriting(t *testing.T) {
	st, p, ids, _, lb := batchWorld(t, 2)
	before := func() int {
		t.Helper()
		rows, err := st.ListLabels(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		return len(rows)
	}
	if got := before(); got != 0 {
		t.Fatalf("%d labels before the batch", got)
	}
	items := []PutItem{
		{MessageID: ids[0], Question: "category", Value: json.RawMessage(`"work"`)},
		{MessageID: ids[1], Question: "not_a_question", Value: json.RawMessage(`1`)},
		{MessageID: ids[1], Question: "category", Value: json.RawMessage(`"not_an_option"`)},
		{MessageID: 999999, Question: "category", Value: json.RawMessage(`"work"`)},
		{MessageID: ids[0], Question: "category", Value: json.RawMessage(`"work"`)},
		{MessageID: ids[0], Question: "reply_needed", Value: json.RawMessage(`{bad`)},
		{MessageID: 0, Question: "", Value: nil},
	}
	_, err := lb.PutBatch(t.Context(), p, items)
	if err == nil {
		t.Fatal("a hostile batch was accepted")
	}
	for _, want := range []string{"unknown question", "value must be one of", "does not exist or is not visible",
		"duplicate", "invalid JSON", "message_id must be positive", "question is required", "value is required"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %q: %v", want, err)
		}
	}
	if got := before(); got != 0 {
		t.Fatalf("a refused batch wrote %d labels", got)
	}
	if _, err := lb.PutBatch(t.Context(), p, nil); err == nil {
		t.Error("an empty batch was accepted")
	}
	if _, err := lb.PutBatch(t.Context(), p, []PutItem{{MessageID: ids[0], Question: "category", Value: json.RawMessage(`"work"`)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := lb.PutBatch(t.Context(), p, []PutItem{
		{MessageID: ids[1], Question: "category", Value: json.RawMessage(`"work"`)},
		{MessageID: ids[1], Question: "category", Value: json.RawMessage(`"spam"`)},
	}); err == nil {
		t.Fatal("a duplicate (message, question) was accepted")
	}
	if got := before(); got != 1 {
		t.Fatalf("a refused batch changed the stored labels: %d", got)
	}
	oversized := make([]PutItem, MaxLabelBatch+1)
	for i := range oversized {
		oversized[i] = PutItem{MessageID: ids[0], Question: "category", Value: json.RawMessage(`"work"`)}
	}
	if _, err := lb.PutBatch(t.Context(), p, oversized); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("an oversized batch: %v", err)
	}
	if got := before(); got != 1 {
		t.Fatalf("an oversized batch wrote labels: %d", got)
	}
}

func TestPutMessageStoresOneCardAndUpserts(t *testing.T) {
	st, p, ids, bus, lb := batchWorld(t, 1)
	changes, err := events.Subscribe(bus, TopicLabel, 8)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]json.RawMessage{
		"category":     json.RawMessage(`"work"`),
		"reply_needed": json.RawMessage(`"yes"`),
		"urgency":      json.RawMessage(`2`),
	}
	got, err := lb.PutMessage(t.Context(), p, ids[0], values)
	if err != nil || len(got.Stored) != 3 {
		t.Fatalf("card %+v %v", got, err)
	}
	change := <-changes.C
	if change.Action != "set" || change.Count != 3 || change.MessageID != ids[0] || change.Question != "" {
		t.Fatalf("a card publishes one event naming its message: %+v", change)
	}
	rows, err := st.ListLabels(t.Context())
	if err != nil || len(rows) != 3 {
		t.Fatalf("stored labels %+v %v", rows, err)
	}
	if got, err := lb.PutMessage(t.Context(), p, ids[0], map[string]json.RawMessage{"category": json.RawMessage(`"spam"`)}); err != nil || len(got.Stored) != 1 {
		t.Fatalf("correction %+v %v", got, err)
	}
	if change := <-changes.C; change.Count != 1 || change.MessageID != ids[0] || change.Question != "category" {
		t.Fatalf("a one-label batch names its message and question: %+v", change)
	}
	rows, err = st.ListLabels(t.Context())
	if err != nil || len(rows) != 3 {
		t.Fatalf("an upsert added a row: %+v %v", rows, err)
	}
	for _, row := range rows {
		if row.Question == "category" && (row.Value != `"spam"` || row.Source != store.LabelManual) {
			t.Fatalf("the correction did not replace the value: %+v", row)
		}
	}
	if _, err := lb.PutMessage(t.Context(), p, ids[0], nil); err == nil {
		t.Fatal("an empty card was accepted")
	}
}
