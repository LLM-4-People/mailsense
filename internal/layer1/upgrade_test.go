package layer1

import (
	"encoding/json"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/store"
)

// Upgrade safety: a stored head reads the rows of one evidence layout. A
// layout change (a new causal signal changes FeatureLayout.ID) makes it
// unfit: the pass retrains and promotes the new-layout head, retiring the
// old one — a candidate that is not measured never keeps an unfit head.
func TestAChangedLayoutNeverKeepsTheOldModel(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 60, nil)
	w.run()
	before, err := w.st.ActiveLayer1(t.Context())
	if err != nil || len(before) != 1 || before[0].Question != "category" {
		t.Fatalf("before: %v %v", before, err)
	}
	var oldMeta modelMeta
	if err := json.Unmarshal([]byte(before[0].Meta), &oldMeta); err != nil {
		t.Fatal(err)
	}

	// Even an unfit head with a perfect recorded metric is not kept when
	// the candidate is unmeasured: the layout check comes first.
	a := &activeModel{id: before[0].ID, model: &Model{Question: "category"}, meta: oldMeta, metrics: &Metrics{NLL: 0}}
	if why := keepActive(a, false, nil, 0.01); why != "" {
		t.Fatalf("an unfit head was kept: %q", why)
	}

	// The layout a causal signal adds changes the cells: a new design.
	if err := w.cfg.Set("evidence.features.hash_buckets", 4); err != nil {
		t.Fatal(err)
	}
	rep := w.run()
	if why, kept := rep.Kept["category"]; kept {
		t.Fatalf("the old layout was kept: %q", why)
	}
	if len(rep.Promoted) != 1 || rep.Promoted[0] != "category" {
		t.Fatalf("promoted %v", rep.Promoted)
	}
	after, err := w.st.ActiveLayer1(t.Context())
	if err != nil || len(after) != 1 || after[0].ID == before[0].ID {
		t.Fatalf("after: %v %v", after, err)
	}
	var newMeta modelMeta
	if err := json.Unmarshal([]byte(after[0].Meta), &newMeta); err != nil {
		t.Fatal(err)
	}
	if newMeta.Layout == oldMeta.Layout || newMeta.Cells == oldMeta.Cells {
		t.Fatalf("the head still reads the old layout: %+v -> %+v", oldMeta, newMeta)
	}
	rows, err := w.st.Layer1Models(t.Context(), "category")
	if err != nil {
		t.Fatal(err)
	}
	retired := false
	for _, row := range rows {
		if row.ID != before[0].ID {
			continue
		}
		var m modelMeta
		if err := json.Unmarshal([]byte(row.Meta), &m); err != nil {
			t.Fatal(err)
		}
		if m.Layout != oldMeta.Layout {
			t.Errorf("the retired head's meta was rewritten to the new layout: %s -> %s", oldMeta.Layout, m.Layout)
		}
		retired = row.State == store.Layer1Retired
	}
	if !retired {
		t.Fatalf("the old head is not retired: %+v", rows)
	}
	if answers := w.answers(); len(answers) != 60 {
		t.Fatalf("answered %d of 60 with the new head", len(answers))
	}
}
