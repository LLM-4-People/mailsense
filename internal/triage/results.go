package triage

import (
	"encoding/json"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/subject"
	"github.com/LLM-4-People/mailsense/internal/triage/taxonomy"
)

// Result is one message's triage as the app shows it.
type Result struct {
	MessageID int64                     `json:"message_id"`
	Values    map[string]taxonomy.Value `json:"values,omitempty"`
	// Subject is the derived topic/subtopic hierarchy. It is consistent
	// with Values by construction and is computed once for every surface
	// that reads this result.
	Subject subject.Subject `json:"subject,omitempty"`
	// Missing lists questions not answered yet (a later chunk failed).
	Missing []string `json:"missing,omitempty"`
	// Stale: decided for an older state of the message; shown, and
	// re-decided by the next run.
	Stale     bool   `json:"stale"`
	DecidedAt string `json:"decided_at,omitempty"`
	// Uncertain lists the escalation roles' answers below the profile's
	// escalation thresholds; Escalate flags the message for the Generator.
	Uncertain []string `json:"uncertain,omitempty"`
	Escalate  bool     `json:"escalate"`
	// Failure is the last failure (a never-decided message has no Values).
	Failure string `json:"failure,omitempty"`
	// Preview: at least one answer shown is the layer-1 model's
	// threshold-free best guess from the explicit test preview, not a
	// certified route answer.
	Preview bool `json:"preview"`
}

// Coverage discloses how much of the eligible mail is judged.
type Coverage struct {
	Eligible int `json:"eligible"`
	Fresh    int `json:"fresh"`      // complete, for the current state
	Partial  int `json:"incomplete"` // current state, some questions missing
	Stale    int `json:"stale"`      // decided for an older state
	Failed   int `json:"failed"`     // last attempt failed (may also be stale)
	Unjudged int `json:"unjudged"`
}

func encodeAnswers(a map[string]engine.Answer) (string, error) {
	b, err := json.Marshal(a)
	return string(b), err
}

// decodeAnswers reads cached raw answers: the profile's questions present
// with their type, and the names missing. An unreadable payload is all
// missing (decided again); an answer that is not a readable engine.Answer
// is missing on its own, so one corrupt answer never discards the others.
// Answers outside the profile are ignored, exactly like the store's
// decision-candidate predicate.
func decodeAnswers(p *taxonomy.Profile, s string) (map[string]engine.Answer, []string) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		raw = nil
	}
	have := map[string]engine.Answer{}
	var missing []string
	for _, q := range p.Questions {
		r, ok := raw[q.Name]
		if !ok {
			missing = append(missing, q.Name)
			continue
		}
		var ans engine.Answer
		if err := json.Unmarshal(r, &ans); err != nil || ans.Type != q.Type {
			missing = append(missing, q.Name)
			continue
		}
		have[q.Name] = ans
	}
	return have, missing
}

// missing returns the questions without an answer in have (all for nil).
func (s *session) missing(have map[string]engine.Answer) []engine.Question {
	if len(have) == 0 {
		return s.profile.Questions
	}
	var out []engine.Question
	for _, q := range s.profile.Questions {
		if _, ok := have[q.Name]; !ok {
			out = append(out, q)
		}
	}
	return out
}
