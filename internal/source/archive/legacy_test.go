package archive

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

var legacyQuestions = []engine.Question{
	{Name: "category", Type: engine.Choice, Instructions: "x", Options: []engine.Option{{Name: "a"}, {Name: "b"}}},
	{Name: "reply_needed", Type: engine.Noul, Instructions: "x"},
	{Name: "urgency", Type: engine.Score, Instructions: "x", Levels: []string{"0", "1", "2"}},
}

func legacyLine(id, fp string, answers string) string {
	answers = strings.Join(strings.Fields(answers), " ") // one JSONL line
	return fmt.Sprintf(`{"message": {"id": %q}, "classification": {"message_id": %q, "fingerprint": %q, "answers": %s}}`, id, id, fp, answers)
}

func legacyStore(t *testing.T, ids ...string) *store.Store {
	t.Helper()
	cfg := configtest.New(t, map[string]any{source.SettingSelected: mailtest.Picked([]string{"legacy"})})
	st, err := store.Open(t.Context(), cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.UpsertSource(t.Context(), "legacy", Kind, "legacy", source.Capabilities{}); err != nil {
		t.Fatal(err)
	}
	var msgs []mail.Message
	for _, id := range ids {
		msgs = append(msgs, mailtest.Message("legacy", id))
	}
	if err := st.UpsertMessages(t.Context(), 0, msgs); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestImportLegacyAnswersReadsTheModelsOwnAnswers(t *testing.T) {
	good := `{"category": {"model_value": null, "value": "b", "prior": 0.9, "probabilities": {"a": 0.3, "b": 0.7}},
		"reply_needed": {"model_value": 0.8, "value": 0.2, "prior": 0.1, "probabilities": null},
		"urgency": {"model_value": 1.1, "probabilities": {"0": 0.2, "1": 0.5, "2": 0.3}}}`
	bad := `{"category": {"probabilities": {"a": 0.3, "z": 0.7}}, "reply_needed": {"model_value": 1.5},
		"urgency": {"probabilities": {"0": 0.2, "1": 0.2}}}`
	path := writeLines(t, true,
		legacyLine("E1", "fp1", good),
		legacyLine("E2", "fp1", bad),
		legacyLine("E3", "fp2", good), // another question set: never imported
		legacyLine("E9", "fp1", good), // no such message
		legacyLine("E4", "fp1", good), // skipped (the harness's dev split)
		"not json",
		legacyLine("E5", "fp1", `{"category": {"probabilities": {"a": 0.5, "b": 0.5}}}`),
	)
	st := legacyStore(t, "E1", "E2", "E3", "E4", "E5")
	key := store.TriageKey{Fingerprint: "fp1", Decider: "legacy:test"}
	rep, err := ImportLegacyAnswers(t.Context(), st, LegacyAnswers{Path: path, SourceID: "legacy", Fingerprint: "fp1", Decider: key.Decider,
		Questions: legacyQuestions, Tolerance: 0.02, Skip: func(id string) bool { return id == "E4" }, MaxLine: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Lines != 7 || rep.Imported != 3 || rep.BadLines != 1 || rep.OtherFingerprint != 1 || rep.NoMessage != 1 || rep.Skipped != 1 ||
		rep.Dropped["category"] != 1 || rep.Dropped["reply_needed"] != 1 || rep.Dropped["urgency"] != 1 || rep.Missing["urgency"] != 1 {
		t.Fatalf("%+v", rep)
	}
	got, err := st.LoadDecisions(t.Context(), key)
	if err != nil || len(got) != 3 {
		t.Fatalf("%d decisions %v", len(got), err)
	}
	var e1 map[string]engine.Answer
	for _, d := range got {
		var a map[string]engine.Answer
		_ = json.Unmarshal([]byte(d.Answers), &a)
		if len(a) == 3 {
			e1 = a
		}
	}
	if e1["category"].Choice != "b" || e1["reply_needed"].Value != 0.8 || e1["urgency"].Value < 1.09 || e1["urgency"].Value > 1.11 {
		t.Fatalf("the model's own answers (never the blend): %+v", e1)
	}
	// the live decider's key is untouched; a second import replaces the first as a whole
	if live, _ := st.LoadDecisions(t.Context(), store.TriageKey{Fingerprint: "fp1", Decider: "subq/subq-snap-1.1"}); len(live) != 0 {
		t.Fatal("legacy answers mixed into the live decider's key")
	}
	if rep, err := ImportLegacyAnswers(t.Context(), st, LegacyAnswers{Path: path, SourceID: "legacy", Fingerprint: "fp1", Decider: key.Decider,
		Questions: legacyQuestions, Tolerance: 0.02, Skip: func(id string) bool { return id != "E5" }, MaxLine: 1 << 20}); err != nil || rep.Imported != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
	if got, _ := st.LoadDecisions(t.Context(), key); len(got) != 1 {
		t.Fatalf("the import is replaced as a whole: %d", len(got))
	}
}

func TestImportLegacyAnswersMisuse(t *testing.T) {
	st := legacyStore(t, "E1")
	for name, o := range map[string]LegacyAnswers{
		"no decider":     {Fingerprint: "fp", Questions: legacyQuestions},
		"no fingerprint": {Decider: "legacy:x", Questions: legacyQuestions},
		"no questions":   {Decider: "legacy:x", Fingerprint: "fp"},
		"no file":        {Decider: "legacy:x", Fingerprint: "fp", Questions: legacyQuestions, Path: "/nonexistent/x.jsonl"},
		// bugs review (plausible): "never a live decider's" was only documented
		"a live decider's key": {Decider: "subq/subq-snap-1.1", Fingerprint: "fp", Questions: legacyQuestions},
		"layer 1's key":        {Decider: "layer1", Fingerprint: "fp", Questions: legacyQuestions},
		"a bare prefix":        {Decider: "legacy:", Fingerprint: "fp", Questions: legacyQuestions},
		"a prefixed live key":  {Decider: "legacy:subq/x", Fingerprint: "fp", Questions: legacyQuestions},
	} {
		if _, err := ImportLegacyAnswers(t.Context(), st, o); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
