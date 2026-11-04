package learning_test

import (
	"context"
	"errors"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/index"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/vector"
)

// probe reports whether the stage answered without reaching the store: a
// canceled context fails every database read, so a cached answer comes back
// nil and a recomputed one fails with context.Canceled.
func probe(t *testing.T, stage learning.Stage) bool {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := stage.Status(ctx)
	switch {
	case err == nil:
		return true
	case errors.Is(err, context.Canceled):
		return false
	default:
		t.Fatalf("%s: %v", stage.ID(), err)
		return false
	}
}

// warm asks every owned stage once and requires each to answer from its
// cache afterwards.
func warm(t *testing.T, w *perfWorld) {
	t.Helper()
	for _, stage := range []learning.Stage{w.mb, w.sy, w.ix.Stage()} {
		if _, err := stage.Status(t.Context()); err != nil {
			t.Fatalf("%s: %v", stage.ID(), err)
		}
		if !probe(t, stage) {
			t.Fatalf("%s was not cached after its first read", stage.ID())
		}
	}
}

// assertCached pins one case of the invalidation matrix: every owned stage
// either answers from its cache (true) or recomputes (false).
func assertCached(t *testing.T, w *perfWorld, mailboxes, sync, embeddings bool) {
	t.Helper()
	for _, tc := range []struct {
		stage learning.Stage
		want  bool
	}{
		{w.mb, mailboxes},
		{w.sy, sync},
		{w.ix.Stage(), embeddings},
	} {
		if got := probe(t, tc.stage); got != tc.want {
			t.Fatalf("%s cached=%v, want %v", tc.stage.ID(), got, tc.want)
		}
	}
}

func (w *perfWorld) firstState(t *testing.T) store.EmbeddingState {
	t.Helper()
	states, err := w.st.EmbeddingStates(t.Context(), nil)
	if err != nil || len(states) == 0 {
		t.Fatalf("states: %d %v", len(states), err)
	}
	return states[0]
}

func TestStageCachesInvalidateOnTheirOwnInputs(t *testing.T) {
	t.Run("mail write", func(t *testing.T) {
		w := newPerfWorld(t, 4, &perfEmbedder{})
		warm(t, w)
		m := mailtest.Message("src", "new")
		if err := w.st.UpsertMessages(t.Context(), 0, []mail.Message{m}); err != nil {
			t.Fatal(err)
		}
		// The sync aggregate and the index coverage read mail; the mailbox
		// stage shares the store's one mail generation, so it recomputes
		// too (it reads no message). A mailbox-specific generation would
		// make that exact; the store has none.
		assertCached(t, w, false, false, false)
	})

	t.Run("detail write", func(t *testing.T) {
		w := newPerfWorld(t, 4, &perfEmbedder{})
		partial := mailtest.Message("src", "partial")
		partial.Partial, partial.Body = true, "preview only"
		if err := w.st.UpsertMessages(t.Context(), 0, []mail.Message{partial}); err != nil {
			t.Fatal(err)
		}
		owners, err := w.st.Owners(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		var verdicts []store.VerdictRow
		if err := w.st.EachMessage(t.Context(), store.MessageFilter{}, func(r *store.MessageRow) error {
			verdicts = append(verdicts, store.VerdictRow{RowID: r.RowID, Verdict: scope.Owner, Reason: scope.ReasonRecipientMatch, Revision: r.Revision})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := w.st.WriteVerdicts(t.Context(), owners[perfKey].Identity.Digest(), verdicts); err != nil {
			t.Fatal(err)
		}
		warm(t, w)
		n, err := w.st.ApplyDetails(t.Context(), 0, "src", []store.DetailUpdate{{
			Key:     source.ItemRef{StoreID: "store-1", ID: "partial"},
			Details: mail.Details{Subject: partial.Subject, Body: "the complete body"},
		}})
		if err != nil || n != 1 {
			t.Fatalf("apply details: %d %v", n, err)
		}
		assertCached(t, w, false, false, false)
	})

	t.Run("embedding save", func(t *testing.T) {
		w := newPerfWorld(t, 4, &perfEmbedder{})
		warm(t, w)
		state := w.firstState(t)
		if err := w.st.SaveEmbeddings(t.Context(), []store.EmbeddingRow{{
			MessageID: state.RowID, Space: "other/space", TextHash: "h", Revision: state.Revision,
			Vector: vector.Quantize([]float32{1}),
		}}); err != nil {
			t.Fatal(err)
		}
		assertCached(t, w, true, true, false)
	})

	t.Run("embedding confirmation", func(t *testing.T) {
		w := newPerfWorld(t, 4, &perfEmbedder{})
		warm(t, w)
		state := w.firstState(t)
		if err := w.st.ConfirmEmbeddings(t.Context(), map[int64]int64{state.RowID: state.Revision}, ""); err != nil {
			t.Fatal(err)
		}
		assertCached(t, w, true, true, false)
	})

	t.Run("embedding failure", func(t *testing.T) {
		w := newPerfWorld(t, 4, &perfEmbedder{})
		warm(t, w)
		state := w.firstState(t)
		if err := w.st.RecordIndexFailures(t.Context(), []store.IndexFailure{{
			MessageID: state.RowID, Revision: state.Revision, Error: "boom",
		}}); err != nil {
			t.Fatal(err)
		}
		assertCached(t, w, true, true, false)
	})

	t.Run("label write", func(t *testing.T) {
		w := newPerfWorld(t, 4, &perfEmbedder{})
		warm(t, w)
		state := w.firstState(t)
		if err := w.st.PutLabel(t.Context(), state.RowID, "category", `"work"`, "manual"); err != nil {
			t.Fatal(err)
		}
		// No owned stage reads labels.
		assertCached(t, w, true, true, true)
	})

	t.Run("settings change a stage reads", func(t *testing.T) {
		w := newPerfWorld(t, 4, &perfEmbedder{})
		warm(t, w)
		if err := w.cfg.Set("index.folder_roles", []string{string(mail.RoleSent)}); err != nil {
			t.Fatal(err)
		}
		// The coverage plan reads the covered roles; the mailbox and sync
		// stages do not.
		assertCached(t, w, true, true, false)
	})

	t.Run("declared owner change", func(t *testing.T) {
		w := newPerfWorld(t, 4, &perfEmbedder{})
		warm(t, w)
		if err := w.cfg.Set(source.SettingDeclared, []any{perfKey + "=other@example.com"}); err != nil {
			t.Fatal(err)
		}
		// MailboxViews and SyncProgress read the owner identity; coverage
		// does not.
		assertCached(t, w, false, false, true)
	})

	t.Run("selection change", func(t *testing.T) {
		w := newPerfWorld(t, 4, &perfEmbedder{})
		warm(t, w)
		if err := w.cfg.Set(source.SettingSelected, []any{source.StoreKey("src", "store-2")}); err != nil {
			t.Fatal(err)
		}
		assertCached(t, w, false, false, false)
	})

	t.Run("index run finish", func(t *testing.T) {
		w := newPerfWorld(t, 4, &perfEmbedder{})
		warm(t, w)
		if rep, err := w.ix.Run(t.Context(), index.Options{}, nil); err != nil {
			t.Fatalf("index run: %+v %v", rep, err)
		}
		// The run wrote vectors (coverage) and a report (unavailable
		// check); neither mailbox stage reads either input.
		assertCached(t, w, true, true, false)
	})
}

// The next Machine.Status sees mail that appeared after the cached build:
// a cached stage must never keep a learned count (or an index coverage)
// from before the write.
func TestNextStatusSeesWorkAfterTheCache(t *testing.T) {
	w := newPerfWorld(t, 4, &perfEmbedder{})
	before := buildStatus(t, w)
	beforeSync, beforeIndex := stage(t, before, learning.StageSync), stage(t, before, learning.StageEmbeddings)
	if beforeSync.Progress.Total != 4 || beforeIndex.Progress.Total != 4 {
		t.Fatalf("before: sync %+v embeddings %+v", beforeSync, beforeIndex)
	}

	m := mailtest.Message("src", "new")
	m.ReceivedAt = "2026-01-01T01:00:00Z"
	if err := w.st.UpsertMessages(t.Context(), 0, []mail.Message{m}); err != nil {
		t.Fatal(err)
	}
	owners, err := w.st.Owners(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var verdicts []store.VerdictRow
	if err := w.st.EachMessage(t.Context(), store.MessageFilter{}, func(r *store.MessageRow) error {
		verdicts = append(verdicts, store.VerdictRow{RowID: r.RowID, Verdict: scope.Owner, Reason: scope.ReasonRecipientMatch, Revision: r.Revision})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.st.WriteVerdicts(t.Context(), owners[perfKey].Identity.Digest(), verdicts); err != nil {
		t.Fatal(err)
	}
	after := buildStatus(t, w)
	if got := stage(t, after, learning.StageSync); got.Progress.Total != 5 || got.Progress.Done != 5 {
		t.Fatalf("the next status kept the old sync count: %+v", got)
	}
	if got := stage(t, after, learning.StageEmbeddings); got.Progress.Total != 5 || got.Progress.Done != 0 {
		t.Fatalf("the next status kept the old coverage: %+v", got)
	}

	// A label write is not an input of either mailbox stage: the counts
	// stay exactly the same.
	if err := w.st.PutLabel(t.Context(), w.firstState(t).RowID, "category", `"work"`, "manual"); err != nil {
		t.Fatal(err)
	}
	labelled := buildStatus(t, w)
	if got := stage(t, labelled, learning.StageSync); got.Progress != stage(t, after, learning.StageSync).Progress {
		t.Fatalf("a label write moved the sync stage: %+v", got)
	}
}

func stage(t *testing.T, st learning.Status, id string) learning.StageStatus {
	t.Helper()
	for _, s := range st.Stages {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("status has no stage %q", id)
	return learning.StageStatus{}
}

// statusProbe wraps one owned stage and records whether its owner cache
// answered a canceled read, then delegates the real read with the status
// build's context. It lets one Machine.Status run to completion while the
// test observes which stages had work to do.
type statusProbe struct {
	learning.Stage
	canceled context.Context
	hit      bool
}

func (p *statusProbe) Status(ctx context.Context) (learning.StageStatus, error) {
	if _, err := p.Stage.Status(p.canceled); err == nil {
		p.hit = true
	}
	return p.Stage.Status(ctx)
}

// probedStatus builds one synchronous Machine.Status with the three owned
// stages wrapped, and returns the cached/recomputed verdict for each.
func probedStatus(t *testing.T, w *perfWorld) (learning.Status, map[string]bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	mb := &statusProbe{Stage: w.mb, canceled: ctx}
	sy := &statusProbe{Stage: w.sy, canceled: ctx}
	ix := &statusProbe{Stage: w.ix.Stage(), canceled: ctx}
	m, err := learning.New(func() string { return perfKey }, w.st, nil, nil,
		mb,
		learning.MachineStage(w.cfg, nil),
		sy,
		learning.NotBuilt(learning.StageEvidence, "perf"),
		ix,
		learning.NotBuilt(learning.StageLabels, "perf"),
		learning.NotBuilt(learning.StageLayer1, "perf"),
		learning.NotBuilt(learning.StageCalibration, "perf"),
		learning.NotBuilt(learning.StageQuestions, "perf"),
	)
	if err != nil {
		t.Fatal(err)
	}
	st, err := m.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return st, map[string]bool{learning.StageMailboxes: mb.hit, learning.StageSync: sy.hit, learning.StageEmbeddings: ix.hit}
}

// The next Machine.Status itself recomputes the affected owned stage and
// only that stage: a canceled read is the query counter, because a cached
// stage answers without reaching the store.
func TestMachineStatusRecomputesOnlyAffectedStages(t *testing.T) {
	cases := []struct {
		name  string
		write func(t *testing.T, w *perfWorld)
		want  map[string]bool // true = answered from the cache
	}{
		{
			name: "embedding save",
			write: func(t *testing.T, w *perfWorld) {
				state := w.firstState(t)
				if err := w.st.SaveEmbeddings(t.Context(), []store.EmbeddingRow{{
					MessageID: state.RowID, Space: "other/space", TextHash: "h", Revision: state.Revision,
					Vector: vector.Quantize([]float32{1}),
				}}); err != nil {
					t.Fatal(err)
				}
			},
			want: map[string]bool{learning.StageMailboxes: true, learning.StageSync: true, learning.StageEmbeddings: false},
		},
		{
			name: "label write",
			write: func(t *testing.T, w *perfWorld) {
				if err := w.st.PutLabel(t.Context(), w.firstState(t).RowID, "category", `"work"`, "manual"); err != nil {
					t.Fatal(err)
				}
			},
			want: map[string]bool{learning.StageMailboxes: true, learning.StageSync: true, learning.StageEmbeddings: true},
		},
		{
			name: "selection change",
			write: func(t *testing.T, w *perfWorld) {
				if err := w.cfg.Set(source.SettingSelected, []any{source.StoreKey("src", "store-2")}); err != nil {
					t.Fatal(err)
				}
			},
			want: map[string]bool{learning.StageMailboxes: false, learning.StageSync: false, learning.StageEmbeddings: false},
		},
		{
			name: "index run finish",
			write: func(t *testing.T, w *perfWorld) {
				if rep, err := w.ix.Run(t.Context(), index.Options{}, nil); err != nil {
					t.Fatalf("index run: %+v %v", rep, err)
				}
			},
			want: map[string]bool{learning.StageMailboxes: true, learning.StageSync: true, learning.StageEmbeddings: false},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newPerfWorld(t, 4, &perfEmbedder{})
			buildStatus(t, w) // warm every owner cache
			tc.write(t, w)
			if _, hits := probedStatus(t, w); !sameHits(hits, tc.want) {
				t.Fatalf("cached stages %v, want %v", hits, tc.want)
			}
		})
	}
}

func sameHits(got, want map[string]bool) bool {
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return len(got) == len(want)
}
