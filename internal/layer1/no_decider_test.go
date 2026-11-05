package layer1

import (
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/triage"
)

// A first-run install has no triage.decider. Training, status and teacher
// choice are local work: they must use the route key's unconfigured identity
// instead of failing on a provider nobody selected yet.
func TestPlanWithoutDeciderKeepsLocalTrainingUsable(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 3, nil)
	if err := w.cfg.Set(triage.DeciderSetting, ""); err != nil {
		t.Fatal(err)
	}
	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatalf("NewPlan with no decider: %v", err)
	}
	if !strings.HasPrefix(plan.DeciderKey, "unconfigured:") {
		t.Fatalf("decider key = %q, want the unconfigured identity", plan.DeciderKey)
	}
	if plan.DeciderTolerance != 0 {
		t.Fatalf("decider tolerance = %v, want 0 without a decider", plan.DeciderTolerance)
	}
	if plan.RouteKey() == "" {
		t.Fatal("the plan has no route key without a decider")
	}
	// Choosing a labels source is local and must not need the decider.
	if err := w.r.ChooseTeacher(t.Context(), TeacherChoice{Question: "category", Source: "manual"}); err != nil {
		t.Fatalf("ChooseTeacher without a decider: %v", err)
	}
	if _, err := NewPlan(w.cfg); err != nil {
		t.Fatalf("NewPlan after a teacher choice without a decider: %v", err)
	}
	// The whole local pass must run too: the job that failed was a layer-1
	// run, not only a plan read.
	if _, err := w.r.Run(t.Context(), nil); err != nil {
		t.Fatalf("layer-1 run without a decider: %v", err)
	}
}
