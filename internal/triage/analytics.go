package triage

import (
	"context"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/feedback"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/threads"
)

// Analytics is everything the Insights view shows, assembled from the
// triage view, the labels and the reply history.
type Analytics struct {
	Coverage     Coverage                        `json:"coverage"`
	Evaluation   feedback.Evaluation             `json:"evaluation"`
	Thresholds   []feedback.Suggestion           `json:"thresholds"`
	Escalation   []feedback.EscalationSuggestion `json:"escalation"`
	Correlations []feedback.Finding              `json:"correlations"`
	Insights     feedback.Insights               `json:"insights"`
	Volume       feedback.Volume                 `json:"volume"`
	Behaviour    *threads.Profile                `json:"behaviour"`
}

// Analytics evaluates the triage against the owner's labels and reply
// history (weak labels: did the owner reply), suggests thresholds, and
// computes correlations, insights and volume patterns. Evaluate, Calibrate
// and CalibrateEscalation apply feedback.Reference themselves: every source
// is loaded, only manual labels score, and Evaluation.Sources reports the
// per-source counts.
func (t *Triager) Analytics(ctx context.Context) (*Analytics, error) {
	v, err := t.Read(ctx)
	if err != nil {
		return nil, err
	}
	zone, err := clock.Zone(t.cfg.String(clock.SettingZone))
	if err != nil {
		return nil, err
	}
	fb := feedback.SettingsFrom(t.cfg)
	judged := v.Judged()
	return &Analytics{Coverage: v.Coverage,
		Evaluation:   feedback.Evaluate(judged, v.labels, weakReplies(v, t.cfg.Duration("feedback.weak_reply.within")), v.Profile),
		Thresholds:   feedback.Calibrate(judged, v.labels, v.Profile, fb.CalibrationStep, fb.CalibrationMin),
		Escalation:   feedback.CalibrateEscalation(judged, v.labels, v.Profile, fb.EscalationAccuracy, fb.CalibrationStep, fb.CalibrationMin),
		Correlations: feedback.Correlations(v.Analysis, judged, v.Profile, fb.Correlations),
		Insights:     feedback.BuildInsights(v.Analysis, v.Behaviour, judged, v.Profile, fb.Insights),
		Volume:       feedback.Patterns(v.Analysis, v.Behaviour, zone, t.cfg.String("calendar.hemisphere")), Behaviour: v.Behaviour}, nil
}

// weakReplies are the weak labels: for every judged owner inbox message,
// whether the owner replied (within the window; 0 = any later reply).
func weakReplies(v *View, within time.Duration) map[int64]bool {
	out := map[int64]bool{}
	for i := range v.Analysis.Messages {
		m := &v.Analysis.Messages[i]
		if r, judged := v.Results[m.RowID]; m.Role == mail.RoleInbox && judged && r.Values != nil {
			reply, replied := v.Analysis.MyReplies[m.RowID]
			out[m.RowID] = replied && (within == 0 || reply.Seconds <= within.Seconds())
		}
	}
	return out
}
