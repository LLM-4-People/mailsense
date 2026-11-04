package api

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/selective"
)

// TestLearningViewSerializesClassCertification: the questions stage's route
// certification report reaches /api/learning with the per-class fields the
// gate will judge in the next round. The class report is additive: the
// endpoint's open/gate answer is unchanged.
func TestLearningViewSerializesClassCertification(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.learning.open = true
	w.learning.status = learning.Status{State: learning.Learned, Stages: []learning.StageStatus{{
		ID:    learning.StageQuestions,
		State: learning.Done,
		Certifications: []learning.QuestionCertification{{
			Question: "suspicious",
			Certification: selective.Certification{
				Target: 0.95, Certified: true, ClassCollapsed: true,
				ClassRecall: []selective.ClassRecall{
					{Class: 0, Eligible: 100, Answered: 100, Correct: 99},
					{Class: 1, Eligible: 4, Answered: 0, Correct: 0},
				},
			},
		}},
	}}}
	code, _, data := do(t, ts, srv, request{method: "GET", path: "/api/learning"})
	mustStatus(t, code, 200, data)
	body := decodeMap(t, data)
	if open, _ := body["open"].(bool); !open {
		t.Fatalf("the class report changed the gate: open = %v", body["open"])
	}
	st, ok := body["status"].(map[string]any)
	if !ok {
		t.Fatalf("status: %T", body["status"])
	}
	stages, ok := st["stages"].([]any)
	if !ok || len(stages) != 1 {
		t.Fatalf("stages: %T %v", st["stages"], st["stages"])
	}
	stage, _ := stages[0].(map[string]any)
	certs, ok := stage["certifications"].([]any)
	if !ok || len(certs) != 1 {
		t.Fatalf("certifications: %T %v", stage["certifications"], stage["certifications"])
	}
	cert, _ := certs[0].(map[string]any)
	if cert["question"] != "suspicious" || cert["class_collapsed"] != true {
		t.Fatalf("certification: %v", cert)
	}
	recall, ok := cert["class_recall"].([]any)
	if !ok || len(recall) != 2 {
		t.Fatalf("class_recall: %T %v", cert["class_recall"], cert["class_recall"])
	}
	minority, _ := recall[1].(map[string]any)
	if minority["class"] != float64(1) || minority["eligible"] != float64(4) ||
		minority["answered"] != float64(0) || minority["correct"] != float64(0) {
		t.Fatalf("minority class: %v", minority)
	}
}
