package layer1

import (
	"encoding/json"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/selective"
)

// TestReportSerializesClassCertification: the stored/published layer-1
// report carries the per-class fields next to the other certification
// fields, not only the learning status view.
func TestReportSerializesClassCertification(t *testing.T) {
	t.Parallel()
	rep := Report{Routes: []RouteReport{{
		Question: "suspicious",
		Certification: selective.Certification{
			Target: 0.95, Certified: true, ClassCollapsed: true,
			ClassRecall: []selective.ClassRecall{{Class: 1, Eligible: 4, Answered: 0, Correct: 0}},
		},
	}}}
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Routes []struct {
			Certification struct {
				ClassCollapsed bool                    `json:"class_collapsed"`
				ClassRecall    []selective.ClassRecall `json:"class_recall"`
			} `json:"certification"`
		} `json:"routes"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Routes) != 1 || !out.Routes[0].Certification.ClassCollapsed {
		t.Fatalf("report JSON lost the collapse flag: %s", raw)
	}
	recall := out.Routes[0].Certification.ClassRecall
	if len(recall) != 1 || recall[0] != (selective.ClassRecall{Class: 1, Eligible: 4}) {
		t.Fatalf("report JSON class recall %+v: %s", recall, raw)
	}
}
