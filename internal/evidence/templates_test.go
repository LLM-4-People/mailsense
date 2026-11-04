package evidence

import (
	"time"

	"testing"

	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/vector"
)

// Near-duplicates of the same sender's earlier mail are counted from the
// stored vectors; later mail, other senders and other spaces never count.
func TestTemplateMatches(t *testing.T) {
	w := newWorld(t, everything, map[string]any{"evidence.template.min_cosine": 0.9})
	w.write(msg("n1", "news@example.net", 0, "a"), msg("n2", "news@example.net", 1, "b"), msg("n3", "news@example.net", 2, "c"),
		msg("odd", "news@example.net", 3, "d"), msg("other", "ann@example.org", 4, "e"), msg("novec", "news@example.net", 5, "f"))
	ids := map[string]int64{}
	_ = w.st.EachMessage(t.Context(), store.MessageFilter{}, func(r *store.MessageRow) error { ids[r.Message.ID] = r.RowID; return nil })
	same, orth := []float32{0.6, 0.8, 0}, []float32{0, 0, 1}
	var rows []store.EmbeddingRow
	for id, v := range map[string][]float32{"n1": same, "n2": same, "n3": same, "odd": orth, "other": same} {
		rows = append(rows, store.EmbeddingRow{MessageID: ids[id], Space: "space-1", TextHash: "h", Vector: vector.Quantize(v)})
	}
	rows = append(rows, store.EmbeddingRow{MessageID: ids["novec"], Space: "space-0", TextHash: "h", Vector: vector.Quantize(same)})
	if err := w.st.SaveEmbeddings(t.Context(), rows); err != nil {
		t.Fatal(err)
	}
	w.run() // no embedder: no space
	want(t, w.record("n2"), "template_matches", Missing(ReasonNoVector))
	w.ev.space = func() (string, error) { return "space-1", nil }
	w.run()
	for id, v := range map[string]Value{"n1": Int(0), "n2": Int(1), "n3": Int(2), "odd": Int(0), "other": Int(0),
		"novec": Missing(ReasonNoVector)} {
		want(t, w.record(id), "template_matches", v)
	}
}

// Regression (review L0, bugs plausible: template window): messages at
// the same instant are not earlier, and they no longer use up the window
// of evidence.template.window earlier messages.
func TestTemplateWindowSkipsTheSameInstant(t *testing.T) {
	same := []float32{0.6, 0.8, 0}
	vecs := map[int64]unitVector{}
	for _, row := range []int64{1, 2, 3} {
		q := vector.Quantize(same)
		vecs[row] = unitVector{values: q.Values, norm: vector.NormInt8(q.Values)}
	}
	group := []templateItem{{row: 1, sender: "a", at: t0}, {row: 2, sender: "a", at: t0.Add(time.Hour)}, {row: 3, sender: "a", at: t0.Add(time.Hour)}}
	got := countGroup(group, vecs, 0.9, 1)
	if got[1] != 0 || got[2] != 1 || got[3] != 1 {
		t.Fatalf("with a window of one, each message at t0+1h sees the earlier one: %v", got)
	}
}
