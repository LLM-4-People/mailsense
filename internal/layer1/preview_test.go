package layer1

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// revision is the message's current revision (the preview row's staleness
// reference, as for a route row).
func (w *world) currentRevision(id int64) int64 {
	w.t.Helper()
	var rev int64
	if err := w.st.EachMessage(w.t.Context(), store.MessageFilter{RowIDs: []int64{id}}, func(r *store.MessageRow) error {
		rev = r.Revision
		return nil
	}); err != nil {
		w.t.Fatal(err)
	}
	return rev
}

// The layer-1 pass writes the threshold-free best guess (selective.Given)
// next to its abstaining answers: the explicit test preview shows it, the
// route does not.
func TestRunWritesThresholdFreePreviewAnswers(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 60, map[string]any{config.SettingPreview: true})
	rep := w.run()
	records, err := w.st.LoadPreviewRecords(t.Context(), w.routeKey(t), w.ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != len(w.ids) {
		t.Fatalf("preview rows %d of %d messages", len(records), len(w.ids))
	}
	answers := w.answers()
	for _, id := range w.ids {
		rec, ok := records[id]
		if !ok {
			t.Fatalf("message %d has no preview row", id)
		}
		if rec.Revision != w.currentRevision(id) || rec.RunID != rep.RunID || rec.DecidedAt == "" {
			t.Fatalf("preview metadata of message %d: %+v", id, rec)
		}
		a, ok := rec.Answers["category"]
		if !ok {
			t.Fatalf("message %d has no category preview: %+v", id, rec.Answers)
		}
		if a.Type != engine.Choice || a.Abstain != "" || a.Refused || a.Choice == "" || a.Confidence == nil || len(a.Probabilities) == 0 {
			t.Fatalf("the preview is not a Given answer: %+v", a)
		}
		if _, other := rec.Answers["urgency"]; other {
			t.Fatal("a question without a model got a preview answer")
		}
		if stored := answers[id]["category"]; stored.Abstain != engine.AbstainBelowTarget {
			t.Fatalf("the stored answer must keep abstaining below its threshold: %+v", stored)
		}
	}
	if other, err := w.st.LoadPreviewRecords(t.Context(), "other-profile", w.ids); err != nil || len(other) != 0 {
		t.Fatalf("another fingerprint sees previews: %v %v", other, err)
	}
}

// A re-score commits through the same path and refreshes the previews
// from the stored layer-1 answers.
func TestRescoreRefreshesPreviewAnswers(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 60, map[string]any{config.SettingPreview: true})
	w.run()
	if n, err := w.st.PrunePreviewAnswers(t.Context(), "other-profile"); err != nil || n == 0 {
		t.Fatalf("prune removed %d: %v", n, err)
	}
	if left, err := w.st.LoadPreviewRecords(t.Context(), w.routeKey(t), w.ids); err != nil || len(left) != 0 {
		t.Fatalf("previews not removed: %d %v", len(left), err)
	}
	rep, err := w.r.Rescore(t.Context())
	if err != nil || !rep.Rescored {
		t.Fatalf("re-score %+v: %v", rep, err)
	}
	records, err := w.st.LoadPreviewRecords(t.Context(), w.routeKey(t), w.ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != len(w.ids) {
		t.Fatalf("the re-score wrote %d preview rows of %d messages", len(records), len(w.ids))
	}
	for _, id := range w.ids {
		a, ok := records[id].Answers["category"]
		if !ok || a.Abstain != "" || a.Choice == "" {
			t.Fatalf("the re-score preview of message %d: %+v", id, a)
		}
	}
}

// The preview table follows learning.test_mode: no rows are written while
// the switch is off, and turning it off clears the rows a previous run left
// behind instead of keeping a second full answer copy on disk.
func TestPreviewAnswersFollowTestMode(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 12, nil)
	load := func() int {
		t.Helper()
		records, err := w.st.LoadPreviewRecords(t.Context(), w.routeKey(t), w.ids)
		if err != nil {
			t.Fatal(err)
		}
		return len(records)
	}
	w.run()
	if got := load(); got != 0 {
		t.Fatalf("test mode off wrote %d preview rows", got)
	}
	if err := w.cfg.Set(config.SettingPreview, true); err != nil {
		t.Fatal(err)
	}
	if _, err := w.r.Rescore(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := load(); got != len(w.ids) {
		t.Fatalf("test mode on wrote %d preview rows of %d", got, len(w.ids))
	}
	if err := w.cfg.Set(config.SettingPreview, false); err != nil {
		t.Fatal(err)
	}
	if _, err := w.r.Rescore(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := load(); got != 0 {
		t.Fatalf("turning the preview off kept %d rows", got)
	}
}
