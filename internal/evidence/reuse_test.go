package evidence

import (
	"slices"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/threads"
)

// Regression (review L0, bugs #3, rules #1): a message-only value that
// reads the scope verdict (direction, addressed_to_me…) is computed again
// when the message is judged again, though the message itself is unchanged.
func TestARejudgedMessageIsComputedAgain(t *testing.T) {
	w := newWorld(t, everything, nil)
	w.write(msg("q1", "ann@example.org", 0, "Hello"))
	w.run()
	want(t, w.record("q1"), "direction", Str("received"))
	row := w.rowOf("q1")
	if err := w.st.WriteVerdicts(t.Context(), "test", []store.VerdictRow{{RowID: row, Verdict: scope.Owner, Reason: scope.ReasonSentByOwner}}); err != nil {
		t.Fatal(err)
	}
	if st := w.status(); st.State == learning.Done {
		t.Fatalf("a new verdict is a new input: %+v", st)
	}
	rep := w.run()
	if rep.Computed == 0 {
		t.Fatalf("the re-judged message is computed again: %+v", rep)
	}
	want(t, w.record("q1"), "direction", Str("sent_by_owner"))
}

// Regression (review L0, bugs #4, rules #1): a source that starts
// declaring a capability makes the stage pending a run, and the run
// computes the signals that needed it.
func TestANewCapabilityIsPickedUp(t *testing.T) {
	caps := everything
	caps.Headers = false
	w := newWorld(t, caps, nil)
	m := msg("q1", "ann@example.org", 0, "Hello")
	m.Headers = "Authentication-Results: mx.example.net; spf=pass smtp.mailfrom=example.org"
	w.write(m)
	w.run()
	want(t, w.record("q1"), "auth_spf", Missing(ReasonCapability+"headers"))
	if st := w.status(); st.State != learning.Done {
		t.Fatalf("done before the change: %+v", st)
	}
	caps.Headers = true
	if err := w.st.UpsertSource(t.Context(), "src", "test", "Test", caps); err != nil {
		t.Fatal(err)
	}
	if st := w.status(); st.State == learning.Done {
		t.Fatalf("a new capability is a new input: %+v", st)
	}
	if rep := w.run(); rep.Computed == 0 || rep.Written == 0 {
		t.Fatalf("the run computes and writes again: %+v", rep)
	}
	want(t, w.record("q1"), "auth_spf", Str("pass"))
}

func (w *world) rowOf(id string) int64 {
	w.t.Helper()
	var row int64
	_ = w.st.EachMessage(w.t.Context(), store.MessageFilter{}, func(r *store.MessageRow) error {
		if r.Message.ID == id {
			row = r.RowID
		}
		return nil
	})
	return row
}

func (w *world) status() learning.StageStatus {
	w.t.Helper()
	st, err := w.ev.Stage().Status(w.t.Context())
	if err != nil {
		w.t.Fatal(err)
	}
	return st
}

// Regression (review L0, bugs #12): a panicking extractor fails the run
// with that message's error (logged with its stack) instead of crashing
// the process; the earlier batches are kept.
func TestAPanickingExtractorFailsTheRunNotTheProcess(t *testing.T) {
	w := newWorld(t, everything, nil)
	w.write(conversation()...)
	w.ev.signals = append(w.ev.signals, Signal{Name: "zz_boom", Version: 1, Type: TypeBool, Group: GroupContent, Doc: "test",
		Extract: func(in *Input) Value {
			if in.Msg.ID == "q4" {
				panic("boom")
			}
			return Bool(true)
		}})
	rep, err := w.ev.Run(t.Context(), nil)
	if err == nil || !strings.Contains(err.Error(), "panic: boom") || rep.State != runstate.Failed {
		t.Fatalf("the panic is the run's error: %v %+v", err, rep)
	}
	if st := w.status(); st.State != learning.Blocked {
		t.Fatalf("the stage shows the failure: %+v", st)
	}
}

// Regression (review L0, bugs plausible: stage status cost): a status call
// over unchanged inputs reuses the fingerprint kept since the last write
// (no full scan); a write computes it again.
func TestStageStatusIsCheapWhileNothingChanged(t *testing.T) {
	w := newWorld(t, everything, nil)
	w.write(conversation()...)
	w.run()
	w.status()
	n := w.ev.stage.computed
	for range 5 {
		if st := w.status(); st.State != learning.Done {
			t.Fatalf("%+v", st)
		}
	}
	if w.ev.stage.computed != n {
		t.Fatalf("unchanged inputs were scanned again: %d -> %d", n, w.ev.stage.computed)
	}
	w.write(msg("new", "zoe@example.net", 90, "Hi"))
	if st := w.status(); st.State == learning.Done || w.ev.stage.computed != n+1 {
		t.Fatalf("a write is a new input: %+v (%d)", st, w.ev.stage.computed)
	}
}

// Regression (review L0, bugs #5): a message still waiting for its details
// (Partial) gets "details pending" for every signal that reads them —
// never complete-looking zeros —, rules do not fire on it, and the stage
// is not done until the details arrive.
func TestPendingDetailsAreNotValues(t *testing.T) {
	w := newWorld(t, everything, nil)
	m := msg("p1", "ann@example.org", 0, "")
	m.Partial, m.Headers, m.Recipients = true, "", nil
	w.write(m)
	w.run()
	r := w.record("p1")
	for _, name := range []string{"question_marks", "date_count", "own_chars", "signature", "language", "auth_dmarc", "recipient_count",
		"addressed_to_me", "attachment_count", "is_reply", "thread_position"} {
		want(t, r, name, Missing(ReasonPending))
	}
	want(t, r, "direction", Str("received"))
	want(t, r, "sender_received_before", Int(0))
	for _, rule := range w.ev.Rules().Rules {
		if rule.Fires(r) {
			t.Errorf("rule %s fired on pending details", rule.ID)
		}
	}
	// The run did everything it can; the partial message is a warning, not
	// a reason to keep the app in "learning" until the owner retries.
	if st := w.status(); st.State != learning.Done || !strings.Contains(st.Note, "waiting for details") {
		t.Fatalf("the stage reports the partial detail as a warning: %+v", st)
	}
	m.Partial, m.Body = false, "Can we meet on 2026-10-20?"
	w.write(m)
	w.run()
	want(t, w.record("p1"), "question_marks", Int(1))
	if st := w.status(); st.State != learning.Done {
		t.Fatalf("complete: %+v", st)
	}
}

// Regression (review L0, bugs #8, rules #4): the settings conversations
// and the history are built with (threads.GroupingSettings) are declared
// by every thread and history signal: a change makes the stage wait for a
// run, and threads.folder_roles (where mail is filed) is no input at all.
func TestGroupingSettingsAreInputs(t *testing.T) {
	w := newWorld(t, everything, nil)
	w.write(conversation()...)
	w.write(msg("sameorg", "eve@example.org", 82, "Hi"))
	w.run()
	want(t, w.record("sameorg"), "first_time_sender", Str("new_address"))
	if err := w.cfg.Set("threads.folder_roles", []any{"inbox"}); err != nil {
		t.Fatal(err)
	}
	if st := w.status(); st.State != learning.Done {
		t.Fatalf("filing roles are no input of the causal view: %+v", st)
	}
	if err := w.cfg.Set("mail.domains.shared", []any{"example.org"}); err != nil {
		t.Fatal(err)
	}
	if st := w.status(); st.State == learning.Done {
		t.Fatalf("a grouping setting is an input: %+v", st)
	}
	w.run()
	want(t, w.record("sameorg"), "first_time_sender", Str("new_address")) // a shared domain: the person is the correspondent
	want(t, w.record("sameorg"), "exchange_years", Int(0))
	for _, s := range Signals() {
		if (s.Group == GroupThread || s.Group == GroupHistory) && s.Context {
			for _, k := range threads.GroupingSettings() {
				if !slices.Contains(s.Settings, k) {
					t.Errorf("%s does not declare %s", s.Name, k)
				}
			}
		}
	}
}

// Regression (review L0, bugs plausible: unbounded tables): runs beyond
// evidence.runs.keep and schemas nothing uses any more are removed after
// every run.
func TestRunsAndSchemasArePruned(t *testing.T) {
	w := newWorld(t, everything, map[string]any{"evidence.runs.keep": 2})
	w.write(conversation()...)
	for range 4 {
		w.run()
	}
	if runs, _ := w.st.ListEvidenceRuns(t.Context(), 100); len(runs) != 2 {
		t.Fatalf("%d runs kept, want 2", len(runs))
	}
	if err := w.cfg.Set(settingMaxDates, 3); err != nil { // a new schema
		t.Fatal(err)
	}
	w.run()
	schemas, err := w.st.EvidenceSchemas(t.Context())
	if err != nil || len(schemas) != 1 {
		t.Fatalf("only the schema in use is kept: %d %v", len(schemas), err)
	}
}

// Regression (review L0, bugs plausible: stale facts): a message changed
// since the last run has no current evidence (an empty record) until the
// next run computes it: an older revision's values are never served.
func TestAChangedMessageHasNoStaleEvidence(t *testing.T) {
	w := newWorld(t, everything, nil)
	m := msg("q1", "ann@example.org", 0, "Can we meet?")
	w.write(m)
	w.run()
	want(t, w.record("q1"), "question_marks", Int(1))
	m.Body = "We meet. Fine."
	w.write(m)
	if r := w.record("q1"); len(r) != 0 {
		t.Fatalf("stale values served: %v", r["question_marks"])
	}
	w.run()
	want(t, w.record("q1"), "question_marks", Int(0))
}
