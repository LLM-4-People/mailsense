package store

import (
	"slices"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/source"
)

func TestPutLabelsIsAtomic(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	ids := seed(t, s, 2)
	if err := s.PutLabels(ctx, []LabelWrite{
		{MessageID: ids[0], Question: "category", Value: `"work"`, Source: LabelManual},
		{MessageID: ids[1], Question: "category", Value: `"spam"`, Source: LabelManual},
	}); err != nil {
		t.Fatal(err)
	}
	if labels, _ := s.ListLabels(ctx); len(labels) != 2 {
		t.Fatalf("batch labels %+v", labels)
	}
	// The row validation happens before any write; a foreign-key failure on
	// the second row rolls the first one back.
	if err := s.PutLabels(ctx, []LabelWrite{
		{MessageID: ids[0], Question: "category", Value: `"action"`, Source: LabelManual},
		{MessageID: 999999, Question: "category", Value: `"work"`, Source: LabelManual},
	}); err == nil {
		t.Fatal("a label for an unknown message was accepted")
	}
	if labels, _ := s.ListLabels(ctx); len(labels) != 2 || labels[0].Value != `"work"` {
		t.Fatalf("a failed batch wrote: %+v", labels)
	}
	if err := s.PutLabels(ctx, []LabelWrite{{MessageID: ids[0], Question: "", Value: `1`, Source: LabelManual}}); err == nil {
		t.Fatal("a malformed row was accepted")
	}
	if err := s.PutLabels(ctx, nil); err == nil {
		t.Fatal("an empty batch was accepted")
	}
}

// Labels are mail: ListLabels applies the one visibility predicate, so an
// unpicked store's labels leave every reader (few-shot examples, layer-1
// inputs, digests) at once and re-picking restores them without data loss.
func TestListLabelsFollowsTheSelection(t *testing.T) {
	keep, drop := source.StoreKey("src", "store-1"), source.StoreKey("src", "store-2")
	s, cfg := open(t, map[string]any{source.SettingSelected: []any{keep, drop}})
	ctx := t.Context()
	a, b := mailtest.Message("src", "a"), mailtest.Message("src", "b")
	b.StoreID = "store-2"
	if err := s.UpsertMessages(ctx, 0, []mail.Message{a, b}); err != nil {
		t.Fatal(err)
	}
	rows := map[string]int64{}
	if err := s.EachMessage(ctx, MessageFilter{}, func(r *MessageRow) error { rows[r.Message.StoreID] = r.RowID; return nil }); err != nil {
		t.Fatal(err)
	}
	for _, l := range []LabelWrite{
		{MessageID: rows["store-1"], Question: "category", Value: `"work"`, Source: LabelManual},
		{MessageID: rows["store-2"], Question: "category", Value: `"spam"`, Source: LabelManual},
	} {
		if err := s.PutLabel(ctx, l.MessageID, l.Question, l.Value, l.Source); err != nil {
			t.Fatal(err)
		}
	}
	values := func() []string {
		t.Helper()
		labels, err := s.ListLabels(ctx)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(labels))
		for i, l := range labels {
			out[i] = l.Value
		}
		slices.Sort(out)
		return out
	}
	if got := values(); !slices.Equal(got, []string{`"spam"`, `"work"`}) {
		t.Fatalf("both picked: %v", got)
	}
	if err := cfg.Set(source.SettingSelected, []any{keep}); err != nil {
		t.Fatal(err)
	}
	if got := values(); !slices.Equal(got, []string{`"work"`}) {
		t.Fatalf("after unpicking store-2: %v", got)
	}
	if err := cfg.Set(source.SettingSelected, []any{drop}); err != nil {
		t.Fatal(err)
	}
	if got := values(); !slices.Equal(got, []string{`"spam"`}) {
		t.Fatalf("after picking only store-2: %v", got)
	}
	if err := cfg.Set(source.SettingSelected, []any{keep, drop}); err != nil {
		t.Fatal(err)
	}
	if got := values(); !slices.Equal(got, []string{`"spam"`, `"work"`}) {
		t.Fatalf("re-picked: %v", got)
	}
	if err := cfg.Set(source.SettingSelected, []any{}); err != nil {
		t.Fatal(err)
	}
	if got := values(); len(got) != 0 {
		t.Fatalf("nothing picked: %v", got)
	}
	if err := cfg.Set(source.SettingSelected, []any{keep, drop}); err != nil {
		t.Fatal(err)
	}
	if got := values(); !slices.Equal(got, []string{`"spam"`, `"work"`}) {
		t.Fatalf("labels must come back after a re-pick: %v", got)
	}
}
