package triage

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// seedMessages writes n owner inbox messages in one batch (row ids in the
// order they were written).
func seedMessages(tb testing.TB, w *world, n int) []int64 {
	tb.Helper()
	msgs := make([]mail.Message, 0, n)
	for i := range n {
		msgs = append(msgs, w.message(fmt.Sprintf("seed-%d", i), mail.RoleInbox, "Inbox", "seed@x.com",
			fmt.Sprintf("c%d", i), fmt.Sprintf("Seed %d", i), base.Add(time.Duration(i)*time.Minute), me))
	}
	w.write(msgs...)
	ids := make([]int64, 0, n)
	if err := w.st.EachMessage(tb.Context(), store.MessageFilter{}, func(r *store.MessageRow) error {
		ids = append(ids, r.RowID)
		return nil
	}); err != nil {
		tb.Fatal(err)
	}
	return ids
}

// seedCurrentDecisions makes every eligible message decided at its current
// revision with every profile question answered: the planner has no work.
func seedCurrentDecisions(tb testing.TB, w *world) {
	tb.Helper()
	s, err := w.tr.prepare(tb.Context())
	if err != nil {
		tb.Fatal(err)
	}
	answers := make(map[string]engine.Answer, len(s.profile.Questions))
	for i := range s.profile.Questions {
		answers[s.profile.Questions[i].Name] = engine.Answer{Type: s.profile.Questions[i].Type}
	}
	encoded, err := encodeAnswers(answers)
	if err != nil {
		tb.Fatal(err)
	}
	var ds []store.Decision
	if err := w.st.EachMessage(tb.Context(), store.MessageFilter{Roles: s.roles, OwnerOnly: true}, func(r *store.MessageRow) error {
		ds = append(ds, store.Decision{MessageID: r.RowID, Revision: r.Revision, StateHash: "seeded", Answers: encoded, RunID: 1})
		return nil
	}); err != nil {
		tb.Fatal(err)
	}
	if err := w.st.SaveDecisions(tb.Context(), s.key, ds); err != nil {
		tb.Fatal(err)
	}
}

func newJobManager(tb testing.TB, w *world) *jobs.Manager {
	tb.Helper()
	m := jobs.NewManager(w.cfg, w.bus, nil)
	tb.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	return m
}

// A no-work sync is answered by the store's existence probe alone: no
// decision row (the answers payload) is loaded and no candidate plan is
// built, so no state is composed for the mailbox.
func TestNoWorkSyncProbesWithoutLoadingDecisions(t *testing.T) {
	w := newWorld(t, map[string]any{SettingAuto: true, SettingBatch: 10})
	seedMessages(t, w, 12)
	seedCurrentDecisions(t, w)
	jm := newJobManager(t, w)

	probes := 0
	pending := w.tr.pending
	w.tr.pending = func(ctx context.Context, sc store.DecisionScope) (bool, error) {
		probes++
		return pending(ctx, sc)
	}
	w.tr.candidates = func(context.Context, store.DecisionScope) ([]int64, error) {
		t.Fatal("a no-work sync built the candidate plan")
		return nil, nil
	}
	w.tr.loadDecisions = func(context.Context, store.TriageKey, []int64) (map[int64]store.Decision, error) {
		t.Fatal("a no-work sync loaded decisions")
		return nil, nil
	}

	j, err := w.tr.StartIfPending(t.Context(), jm, Options{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if j != nil {
		t.Fatal("a fully decided mailbox started a triage job")
	}
	if probes != 1 {
		t.Fatalf("the no-work path ran %d probes, want exactly 1", probes)
	}
}

// A work sync loads only the candidate rows: the mailbox's other decisions
// are never read. The candidate decision is written exactly as before.
func TestWorkSyncLoadsOnlyTheCandidates(t *testing.T) {
	w := newWorld(t, map[string]any{SettingAuto: true, SettingBatch: 10})
	seedMessages(t, w, 12)
	seedCurrentDecisions(t, w)
	newID := w.mail("new@client.com", "new", "New", time.Hour, 0)
	jm := newJobManager(t, w)

	var mu sync.Mutex
	probes := 0
	var loaded [][]int64
	pending := w.tr.pending
	w.tr.pending = func(ctx context.Context, sc store.DecisionScope) (bool, error) {
		mu.Lock()
		probes++
		mu.Unlock()
		return pending(ctx, sc)
	}
	load := w.tr.loadDecisions
	w.tr.loadDecisions = func(ctx context.Context, k store.TriageKey, rows []int64) (map[int64]store.Decision, error) {
		mu.Lock()
		loaded = append(loaded, append([]int64(nil), rows...))
		mu.Unlock()
		return load(ctx, k, rows)
	}

	j, err := w.tr.StartIfPending(t.Context(), jm, Options{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if j == nil {
		t.Fatal("a new message did not start triage")
	}
	<-j.Done()

	mu.Lock()
	defer mu.Unlock()
	if probes != 1 {
		t.Fatalf("the work path ran %d probes, want exactly 1", probes)
	}
	if len(loaded) == 0 {
		t.Fatal("the work path loaded no decision")
	}
	for _, rows := range loaded {
		if len(rows) != 1 || rows[0] != newID {
			t.Fatalf("loaded decisions for %v, want only the new message %d", rows, newID)
		}
	}
	s, err := w.tr.prepare(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	stored, err := w.st.LoadDecisions(t.Context(), s.key)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stored[newID]; !ok {
		t.Fatalf("the new message's decision was not written: %+v", stored)
	}
	if len(stored) != 13 {
		t.Fatalf("stored %d decisions, want the 12 seeded + the new one", len(stored))
	}
}

// A changed decision key (fingerprint/decider) is a new decision space: the
// probe sees work and the row is decided under the new key. A derived-data
// change leaves the paid row current for the message, so a default run does
// nothing; Redecide sees the stale row, refreshes it, and only then is the
// row current again.
func TestKeyAndStaleChangesRefreshTheRow(t *testing.T) {
	w := newWorld(t, map[string]any{SettingAuto: true, SettingBatch: 10})
	id := w.mail("a@client.com", "A", "Hi", 0, 0)
	w.run(Options{})
	first, err := w.tr.prepare(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if stored, _ := w.st.LoadDecisions(t.Context(), first.key); stored[id].MessageID != id {
		t.Fatalf("the first run stored no decision under its key: %+v", stored)
	}

	// The decider key changes: every eligible message is unseen there.
	w.dec.name = "other"
	jm := newJobManager(t, w)
	j, err := w.tr.StartIfPending(t.Context(), jm, Options{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if j == nil {
		t.Fatal("a new decision key did not start triage")
	}
	<-j.Done()
	s, err := w.tr.prepare(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if stored, _ := w.st.LoadDecisions(t.Context(), s.key); stored[id].MessageID != id {
		t.Fatalf("the changed key's row was not written: %+v", stored)
	}

	// A derived-data change (evidence facts join the states) does not change
	// the message revision: the row stays valid for a default run.
	before, err := w.st.LoadDecisions(t.Context(), s.key)
	if err != nil {
		t.Fatal(err)
	}
	w.tr.UseEvidence(&fakeEvidence{facts: map[int64]string{id: "list_id: no"}, version: "1"})
	if err := w.cfg.Set("triage.facts.evidence", true); err != nil {
		t.Fatal(err)
	}
	pending, err := w.tr.hasPending(t.Context(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if pending {
		t.Fatal("a derived-data change asked the paid plan for work")
	}
	if j, err := w.tr.StartIfPending(t.Context(), jm, Options{Limit: 10}); err != nil || j != nil {
		t.Fatalf("a derived-data change started triage: job=%v err=%v", j, err)
	}
	// The user's explicit re-decide refreshes the same row.
	j, err = w.tr.StartIfPending(t.Context(), jm, Options{Limit: 10, Redecide: true})
	if err != nil {
		t.Fatal(err)
	}
	if j == nil {
		t.Fatal("Redecide did not start triage")
	}
	<-j.Done()
	after, err := w.st.LoadDecisions(t.Context(), s.key)
	if err != nil {
		t.Fatal(err)
	}
	if after[id].StateHash == before[id].StateHash || after[id].RunID == before[id].RunID {
		t.Fatalf("the stale row was not refreshed: before %+v after %+v", before[id], after[id])
	}
}

// A failed first decision leaves no decision row, so it stays a candidate
// and the next run retries it exactly as before.
func TestFailedDecisionIsRetried(t *testing.T) {
	w := newWorld(t, map[string]any{SettingAuto: true, SettingBatch: 10})
	id := w.mail("a@client.com", "A", "Hi", 0, 0)
	w.dec.fail = func(string) error { return fmt.Errorf("contract: missing answer") }
	if rep := w.run(Options{}); rep.Failed != 1 || rep.Decided != 0 {
		t.Fatalf("first attempt: %+v", rep)
	}
	pending, err := w.tr.hasPending(t.Context(), Options{})
	if err != nil || !pending {
		t.Fatalf("a failed message is not a candidate: pending=%v err=%v", pending, err)
	}
	w.dec.fail = nil
	jm := newJobManager(t, w)
	j, err := w.tr.StartIfPending(t.Context(), jm, Options{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if j == nil {
		t.Fatal("the failed message was not retried")
	}
	<-j.Done()
	s, err := w.tr.prepare(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if stored, _ := w.st.LoadDecisions(t.Context(), s.key); stored[id].MessageID != id {
		t.Fatalf("the retry stored no decision: %+v", stored)
	}
	if failures, _ := w.st.LoadFailures(t.Context(), s.key); len(failures) != 0 {
		t.Fatalf("the successful retry left failures: %+v", failures)
	}
}

// The probe's SQL predicate is the same rule as the planner's Go freshness
// rule: never decided, another message revision, or an answer set that is
// unreadable or missing a profile question. A decision the probe calls
// current can never be pending for a default run.
func TestCandidateProbeMatchesTheDecisionRules(t *testing.T) {
	cases := []struct {
		name    string
		answers func(testing.TB, *session) string // nil: never decided
		rev     func(current int64) int64
	}{
		{"never decided", nil, nil},
		{"current complete", func(tb testing.TB, s *session) string { return completeAnswers(tb, s) }, func(rev int64) int64 { return rev }},
		{"revision changed", func(tb testing.TB, s *session) string { return completeAnswers(tb, s) }, func(rev int64) int64 { return rev + 1 }},
		{"missing question", func(tb testing.TB, s *session) string {
			return answersJSON(tb, s, s.profile.Questions[0].Name, "")
		}, func(rev int64) int64 { return rev }},
		{"wrong type", func(tb testing.TB, s *session) string {
			return answersJSON(tb, s, "", s.profile.Questions[0].Name)
		}, func(rev int64) int64 { return rev }},
		{"malformed", func(testing.TB, *session) string { return "{not json" }, func(rev int64) int64 { return rev }},
		{"not an object", func(testing.TB, *session) string { return "[]" }, func(rev int64) int64 { return rev }},
		{"null", func(testing.TB, *session) string { return "null" }, func(rev int64) int64 { return rev }},
		{"extra answer", func(tb testing.TB, s *session) string {
			return answersJSON(tb, s, "", "")
		}, func(rev int64) int64 { return rev }},
		{"null extra answer", func(tb testing.TB, s *session) string {
			return rawAnswers(tb, s, func(m map[string]any) { m["not-a-profile-question"] = nil })
		}, func(rev int64) int64 { return rev }},
		{"null profile answer", func(tb testing.TB, s *session) string {
			return rawAnswers(tb, s, func(m map[string]any) { m[s.profile.Questions[0].Name] = nil })
		}, func(rev int64) int64 { return rev }},
		{"wrong answer type", func(tb testing.TB, s *session) string {
			return rawAnswers(tb, s, func(m map[string]any) {
				m[s.profile.Questions[0].Name] = map[string]any{"type": 1}
			})
		}, func(rev int64) int64 { return rev }},
		{"bad value field", func(tb testing.TB, s *session) string {
			return rawAnswers(tb, s, func(m map[string]any) {
				m[s.profile.Questions[0].Name] = map[string]any{"type": string(s.profile.Questions[0].Type), "value": "x"}
			})
		}, func(rev int64) int64 { return rev }},
		{"bad refused field", func(tb testing.TB, s *session) string {
			return rawAnswers(tb, s, func(m map[string]any) {
				m[s.profile.Questions[0].Name] = map[string]any{"type": string(s.profile.Questions[0].Type), "refused": "yes"}
			})
		}, func(rev int64) int64 { return rev }},
		{"bad probabilities", func(tb testing.TB, s *session) string {
			return rawAnswers(tb, s, func(m map[string]any) {
				m[s.profile.Questions[0].Name] = map[string]any{"type": string(s.profile.Questions[0].Type), "probabilities": map[string]any{"x": "y"}}
			})
		}, func(rev int64) int64 { return rev }},
		{"bad extra answer", func(tb testing.TB, s *session) string {
			return rawAnswers(tb, s, func(m map[string]any) { m["not-a-profile-question"] = "x" })
		}, func(rev int64) int64 { return rev }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t, nil)
			id := w.mail("a@client.com", "A", "Hi", 0, 0)
			s, err := w.tr.prepare(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			rev := w.revision(t, id)
			want := true
			if tc.answers != nil {
				answers := tc.answers(t, s)
				_, missing := decodeAnswers(s.profile, answers)
				drev := rev
				if tc.rev != nil {
					drev = tc.rev(rev)
				}
				want = drev != rev || len(missing) > 0
				if err := w.st.SaveDecision(t.Context(), s.key, store.Decision{MessageID: id, Revision: drev,
					StateHash: "h", Answers: answers, RunID: 1}); err != nil {
					t.Fatal(err)
				}
			}
			sc := w.tr.decisionScope(s, Options{})
			got, err := w.st.HasPendingDecisions(t.Context(), sc)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				n, _ := w.st.DecisionCount(t.Context(), sc)
				stored, _ := w.st.LoadDecisions(t.Context(), s.key)
				t.Fatalf("probe = %v, want %v (eligible %d stored %+v scope %+v)", got, want, n, stored, sc)
			}
			rows, err := w.st.DecisionCandidates(t.Context(), sc)
			if err != nil {
				t.Fatal(err)
			}
			if candidate := len(rows) == 1; candidate != want {
				t.Fatalf("candidates = %v, want the probe's %v", rows, want)
			}
		})
	}
}

// completeAnswers answers every profile question with its own type.
func completeAnswers(tb testing.TB, s *session) string {
	tb.Helper()
	return answersJSON(tb, s, "", "")
}

// answersJSON answers every profile question except skip (when non-empty),
// giving mismatch (when non-empty) another type, and adds one answer
// outside the profile: decodeAnswers ignores extras.
func answersJSON(tb testing.TB, s *session, skip, mismatch string) string {
	tb.Helper()
	return rawAnswers(tb, s, func(m map[string]any) {
		if skip != "" {
			delete(m, skip)
		}
		if mismatch != "" {
			m[mismatch] = map[string]any{"type": "mismatched"}
		}
	})
}

// rawAnswers is a raw answer payload for every profile question plus one
// answer outside the profile; edit mutates it before it is encoded.
func rawAnswers(tb testing.TB, s *session, edit func(map[string]any)) string {
	tb.Helper()
	m := make(map[string]any, len(s.profile.Questions)+1)
	for i := range s.profile.Questions {
		q := &s.profile.Questions[i]
		m[q.Name] = map[string]any{"type": string(q.Type)}
	}
	m["not-a-profile-question"] = map[string]any{"type": string(engine.Noul)}
	if edit != nil {
		edit(m)
	}
	encoded, err := json.Marshal(m)
	if err != nil {
		tb.Fatal(err)
	}
	return string(encoded)
}

// BenchmarkStartIfPendingNoWork pins the idle-sync cost: one existence
// probe, zero decision rows loaded and zero states composed, independent of
// the mailbox size.
func BenchmarkStartIfPendingNoWork(b *testing.B) {
	w := newWorld(b, map[string]any{SettingAuto: true, SettingBatch: 10})
	seedMessages(b, w, 1000)
	seedCurrentDecisions(b, w)
	jm := newJobManager(b, w)

	var probes, loaded atomic.Int64
	pending := w.tr.pending
	w.tr.pending = func(ctx context.Context, sc store.DecisionScope) (bool, error) {
		probes.Add(1)
		return pending(ctx, sc)
	}
	load := w.tr.loadDecisions
	w.tr.loadDecisions = func(ctx context.Context, k store.TriageKey, rows []int64) (map[int64]store.Decision, error) {
		loaded.Add(int64(len(rows)))
		return load(ctx, k, rows)
	}

	b.ResetTimer()
	for range b.N {
		j, err := w.tr.StartIfPending(b.Context(), jm, Options{Limit: 10})
		if err != nil || j != nil {
			b.Fatalf("no-work sync: job=%v err=%v", j, err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(probes.Load())/float64(b.N), "probes/op")
	b.ReportMetric(float64(loaded.Load())/float64(b.N), "decision-rows/op")
}
