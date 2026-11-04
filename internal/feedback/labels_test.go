package feedback

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

func TestPutAndLoadLabels(t *testing.T) {
	cfg := configtest.New(t, map[string]any{source.SettingSelected: mailtest.Picked([]string{"src"})})
	st, err := store.Open(t.Context(), cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := t.Context()
	_ = st.UpsertSource(ctx, "src", "test", "T", source.Capabilities{})
	if err := st.UpsertMessages(ctx, 0, []mail.Message{mailtest.Message("src", "m1")}); err != nil {
		t.Fatal(err)
	}
	var id int64
	_ = st.EachMessage(ctx, store.MessageFilter{}, func(r *store.MessageRow) error { id = r.RowID; return nil })
	p := profile(t)
	bus := events.New()
	defer bus.Close()
	changes, _ := events.Subscribe(bus, TopicLabel, 8)
	lb := NewLabeler(st, bus)
	if err := lb.Put(ctx, p, id, "category", "spam"); err != nil {
		t.Fatal(err)
	}
	if err := lb.Put(ctx, p, id, "reply_needed", "yes"); err != nil {
		t.Fatal(err)
	}
	if c := <-changes.C; c.Action != "set" || c.Question != "category" {
		t.Fatalf("label event %+v", c)
	}
	for q, raw := range map[string]any{"category": "nope", "urgency": 9.0, "unknown": 1.0} {
		if err := lb.Put(ctx, p, id, q, raw); err == nil {
			t.Errorf("%s=%v accepted", q, raw)
		}
	}
	// A label stored under a question the profile later dropped is skipped, not mixed in.
	_ = st.PutLabel(ctx, id, "retired_question", "1", store.LabelManual)
	ls, skipped, err := Load(ctx, st, p)
	if err != nil || len(ls) != 2 || skipped != 1 || ls[0].Sender != "elodie@example.org" || ls[1].Value.Number != 1 || ls[0].At.IsZero() || ls[0].Created.IsZero() {
		t.Fatalf("labels %+v skipped %d %v", ls, skipped, err)
	}
	if err := lb.Delete(ctx, id, "category"); err != nil {
		t.Fatal(err)
	}
	<-changes.C
	if c := <-changes.C; c.Action != "delete" {
		t.Fatalf("unlabel event %+v", c)
	}
	if ls, _, _ = Load(ctx, st, p); len(ls) != 1 {
		t.Fatal("unlabelled")
	}
}

// The few-shot labels are learning input: a store the owner unpicked must
// not teach the learner, and re-picking its mail must bring its labels back.
func TestLoadLabelsNeverCrossesAnUnpickedStore(t *testing.T) {
	keep, drop := source.StoreKey("src", "store-1"), source.StoreKey("src", "store-2")
	cfg := configtest.New(t, map[string]any{source.SettingSelected: mailtest.Picked([]string{"src"}, [2]string{"src", "store-2"})})
	st, err := store.Open(t.Context(), cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := t.Context()
	_ = st.UpsertSource(ctx, "src", "test", "T", source.Capabilities{})
	a, b := mailtest.Message("src", "m1"), mailtest.Message("src", "m2")
	b.StoreID = "store-2"
	if err := st.UpsertMessages(ctx, 0, []mail.Message{a, b}); err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	if err := st.EachMessage(ctx, store.MessageFilter{}, func(r *store.MessageRow) error { ids[r.Message.StoreID] = r.RowID; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := st.PutLabel(ctx, ids["store-1"], "category", `"work"`, store.LabelManual); err != nil {
		t.Fatal(err)
	}
	if err := st.PutLabel(ctx, ids["store-2"], "category", `"spam"`, store.LabelManual); err != nil {
		t.Fatal(err)
	}
	p := profile(t)
	if ls, _, err := Load(ctx, st, p); err != nil || len(ls) != 2 {
		t.Fatalf("both picked: %d labels, %v", len(ls), err)
	}
	if err := cfg.Set(source.SettingSelected, []any{keep}); err != nil {
		t.Fatal(err)
	}
	ls, _, err := Load(ctx, st, p)
	if err != nil || len(ls) != 1 || ls[0].Value.Choice != "work" || ls[0].MessageID != ids["store-1"] {
		t.Fatalf("an unpicked store's label reached the learner: %d labels, %v", len(ls), err)
	}
	if err := cfg.Set(source.SettingSelected, []any{drop}); err != nil {
		t.Fatal(err)
	}
	if ls, _, err = Load(ctx, st, p); err != nil || len(ls) != 1 || ls[0].Value.Choice != "spam" || ls[0].MessageID != ids["store-2"] {
		t.Fatalf("the picked store's label is missing: %d labels, %v", len(ls), err)
	}
	if err := cfg.Set(source.SettingSelected, []any{keep, drop}); err != nil {
		t.Fatal(err)
	}
	if ls, _, err = Load(ctx, st, p); err != nil || len(ls) != 2 {
		t.Fatalf("re-picked labels did not come back: %d labels, %v", len(ls), err)
	}
}

// A judge batch is one write path with its own source and prompt digest:
// one event per message, a manual answer for the same question coexists,
// and the same prompt replaces rather than duplicates.
func TestPutJudgeStoresItsSourcePromptAndOneEvent(t *testing.T) {
	cfg := configtest.New(t, map[string]any{source.SettingSelected: mailtest.Picked([]string{"src"})})
	st, err := store.Open(t.Context(), cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := t.Context()
	_ = st.UpsertSource(ctx, "src", "test", "T", source.Capabilities{})
	if err := st.UpsertMessages(ctx, 0, []mail.Message{mailtest.Message("src", "m1")}); err != nil {
		t.Fatal(err)
	}
	var id int64
	_ = st.EachMessage(ctx, store.MessageFilter{}, func(r *store.MessageRow) error { id = r.RowID; return nil })
	p := profile(t)
	bus := events.New()
	defer bus.Close()
	changes, _ := events.Subscribe(bus, TopicLabel, 8)
	lb := NewLabeler(st, bus)
	res, err := lb.PutJudge(ctx, p, id, "prompt-digest-1", map[string]taxonomy.Label{"category": {Choice: "work"}})
	if err != nil || len(res.Stored) != 1 || res.Stored[0].Source != store.LabelJudge {
		t.Fatalf("put judge %+v %v", res, err)
	}
	if c := <-changes.C; c.Action != "set" || c.Count != 1 || c.MessageID != id || c.Question != "category" {
		t.Fatalf("judge event %+v", c)
	}
	rows, err := st.ListLabels(ctx)
	if err != nil || len(rows) != 1 || rows[0].Source != store.LabelJudge || rows[0].Prompt != "prompt-digest-1" || rows[0].Value != `"work"` {
		t.Fatalf("rows %+v %v", rows, err)
	}
	if err := lb.Put(ctx, p, id, "category", "spam"); err != nil {
		t.Fatal(err)
	}
	<-changes.C
	rows, _ = st.ListLabels(ctx)
	if len(rows) != 2 || rows[0].Source != store.LabelJudge || rows[1].Source != store.LabelManual {
		t.Fatalf("manual and judge must coexist: %+v", rows)
	}
	if _, err := lb.PutJudge(ctx, p, id, "prompt-digest-1", map[string]taxonomy.Label{"category": {Choice: "spam"}}); err != nil {
		t.Fatal(err)
	}
	<-changes.C
	if rows, _ = st.ListLabels(ctx); len(rows) != 2 || rows[0].Prompt != "prompt-digest-1" || rows[0].Value != `"spam"` {
		t.Fatalf("the same prompt duplicated the judge row: %+v", rows)
	}
	if _, err := lb.PutJudge(ctx, p, id, "", map[string]taxonomy.Label{"category": {Choice: "work"}}); err == nil {
		t.Fatal("a judge batch without its prompt digest was accepted")
	}
	if _, err := lb.PutJudge(ctx, p, id, "d", map[string]taxonomy.Label{"nope": {Number: 1}}); err == nil {
		t.Fatal("a judge label for an unknown question was accepted")
	}
	if _, err := lb.PutJudge(ctx, p, id, "d", nil); err == nil {
		t.Fatal("an empty judge batch was accepted")
	}
}
