package layer1

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/triage"
)

func teacherDoc(t *testing.T, w *world) ([]byte, map[string]any) {
	t.Helper()
	raw, err := os.ReadFile(w.cfg.Paths().Resolve(w.cfg.String(SettingTeachersFile)))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return raw, doc
}

// ChooseTeacher re-enables one source for one question, keeps every other
// override, and is idempotent; the merged document still loads.
func TestChooseTeacherPreservesOtherOverrides(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 3, nil)
	initial := `{"sources": {"judge": {"from": "labels", "label_source": "judge", "precedence": 2, "weight": 3, "eval": true}}, "questions": {"category": {"judge": null}, "topic": {"decider": null}}}`
	if err := writeTeachers(w.cfg, initial); err != nil {
		t.Fatal(err)
	}
	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(plan.Teachers.For("category"), func(s Source) bool { return s.Name == "judge" }) {
		t.Fatal("the test did not remove judge from category")
	}
	if err := w.r.ChooseTeacher(t.Context(), TeacherChoice{Question: "category", Source: "judge"}); err != nil {
		t.Fatal(err)
	}
	raw, doc := teacherDoc(t, w)
	sources := doc["sources"].(map[string]any)
	judge := sources["judge"].(map[string]any)
	if judge["weight"] != float64(3) {
		t.Fatalf("the source override was lost: %+v", judge)
	}
	questions := doc["questions"].(map[string]any)
	topic := questions["topic"].(map[string]any)
	if value, ok := topic["decider"]; !ok || value != nil {
		t.Fatalf("another question's override was lost: %+v", topic)
	}
	category := questions["category"].(map[string]any)
	added := category["judge"].(map[string]any)
	if added["from"] != "labels" || added["label_source"] != "judge" || added["weight"] != float64(3) || added["eval"] != true {
		t.Fatalf("the chosen source: %+v", added)
	}
	plan, err = NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(plan.Teachers.For("category"), func(s Source) bool { return s.Name == "judge" }) {
		t.Fatal("the chosen source does not teach the question")
	}
	if err := w.r.ChooseTeacher(t.Context(), TeacherChoice{Question: "category", Source: "judge"}); err != nil {
		t.Fatal(err)
	}
	again, _ := teacherDoc(t, w)
	if string(again) != string(raw) {
		t.Fatalf("a repeated choice changed the file:\n%s\n%s", raw, again)
	}
}

// A missing teachers file is created with only the chosen override; the
// shipped sources stay in force.
func TestChooseTeacherCreatesTheFile(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 3, nil)
	path := w.cfg.Paths().Resolve(w.cfg.String(SettingTeachersFile))
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := w.r.ChooseTeacher(t.Context(), TeacherChoice{Question: "category", Source: "judge"}); err != nil {
		t.Fatal(err)
	}
	_, doc := teacherDoc(t, w)
	questions := doc["questions"].(map[string]any)
	if _, ok := questions["category"].(map[string]any)["judge"].(map[string]any); !ok {
		t.Fatalf("the created file %+v", doc)
	}
	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(plan.Teachers.For("category"), func(s Source) bool { return s.Name == "judge" }) {
		t.Fatal("the created file does not teach the question")
	}
}

// The decider source is the triage.decider setting, validated against the
// catalog; no other decision source is a one-click choice.
func TestChooseTeacherSetsTheDecider(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 3, nil)
	if err := w.r.ChooseTeacher(t.Context(), TeacherChoice{Question: "category", Source: "decider", Provider: "clef"}); err != nil {
		t.Fatal(err)
	}
	if got := w.cfg.String(triage.DeciderSetting); got != "clef" {
		t.Fatalf("triage.decider %q", got)
	}
	plan, err := NewPlan(w.cfg)
	if err != nil || !strings.HasPrefix(plan.DeciderKey, "clef/") {
		t.Fatalf("plan %+v %v", plan, err)
	}
	for _, provider := range []string{"embeddinggemma", "nope"} {
		if err := w.r.ChooseTeacher(t.Context(), TeacherChoice{Question: "category", Source: "decider", Provider: provider}); err == nil {
			t.Errorf("provider %q was accepted", provider)
		}
		if got := w.cfg.String(triage.DeciderSetting); got != "clef" {
			t.Fatalf("a refused choice changed the decider to %q", got)
		}
	}
	if err := w.r.ChooseTeacher(t.Context(), TeacherChoice{Question: "category", Source: "decider"}); err == nil {
		t.Error("a decider choice without a provider was accepted")
	}
}

// Every misuse is refused with every problem at once and writes nothing
// (no file is created for a refused choice).
func TestChooseTeacherRefusesMisuseWithoutWriting(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 3, nil)
	path := w.cfg.Paths().Resolve(w.cfg.String(SettingTeachersFile))
	snapshot := func() ([]byte, bool) {
		t.Helper()
		raw, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return nil, false
		}
		if err != nil {
			t.Fatal(err)
		}
		return raw, true
	}
	before, existed := snapshot()
	decider := w.cfg.String(triage.DeciderSetting)
	for _, choice := range []TeacherChoice{
		{Question: "not_a_question", Source: "judge"},
		{Question: "category", Source: "not_a_source"},
		{Question: "category", Source: "judge", Provider: "subq"},
		{Question: "category", Source: "decider"},
		{Question: "category", Source: "decider", Provider: "embeddinggemma"},
		{Question: "category", Source: "decider", Provider: "nope"},
	} {
		if err := w.r.ChooseTeacher(t.Context(), choice); err == nil {
			t.Errorf("choice %+v was accepted", choice)
		}
		now, nowExists := snapshot()
		if nowExists != existed || string(now) != string(before) {
			t.Fatalf("a refused choice changed the teachers file: %+v", choice)
		}
		if got := w.cfg.String(triage.DeciderSetting); got != decider {
			t.Fatalf("a refused choice changed the decider to %q", got)
		}
	}
	err := w.r.ChooseTeacher(t.Context(), TeacherChoice{Question: "not_a_question", Source: "not_a_source", Provider: "nope"})
	if err == nil || !strings.Contains(err.Error(), "unknown question") || !strings.Contains(err.Error(), "unknown teacher source") ||
		!strings.Contains(err.Error(), "provider applies only") {
		t.Fatalf("not every problem: %v", err)
	}
	if err := writeTeachers(w.cfg, `{bad`); err != nil {
		t.Fatal(err)
	}
	broken, _ := snapshot()
	if err := w.r.ChooseTeacher(t.Context(), TeacherChoice{Question: "category", Source: "judge"}); err == nil {
		t.Fatal("a broken existing file was overwritten")
	}
	if now, _ := snapshot(); string(now) != string(broken) {
		t.Fatal("a refused choice rewrote the broken file")
	}
}
