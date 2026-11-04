package layer1

import (
	"encoding/json"
	"testing"
)

// Only the newest run's label digests are read. Retaining a full
// mailbox-sized array on every kept run grows the database without bound;
// a later commit strips the older copies.
func TestOlderRunsKeepNoLabelDigests(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 30, nil)
	first := w.run()
	second := w.run()
	if len(first.Labels) == 0 || len(second.Labels) == 0 {
		t.Skip("the fixture has no label digests")
	}
	runs, err := w.st.ListLayer1Runs(t.Context(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) < 2 {
		t.Fatalf("layer1 runs = %d, want 2", len(runs))
	}
	var newest, older Report
	if err := json.Unmarshal([]byte(runs[0].Report), &newest); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(runs[1].Report), &older); err != nil {
		t.Fatal(err)
	}
	if len(newest.Labels) == 0 {
		t.Fatal("the newest run lost its label digests")
	}
	if len(older.Labels) != 0 {
		t.Fatalf("an older run kept %d label digests", len(older.Labels))
	}
}
