package evidence

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
)

// One conversation: the sender asks, the owner answers with a question,
// the sender answers, then writes again twice without an answer.
func conversation() []mail.Message {
	thread := func(m mail.Message, parent string) mail.Message {
		m.ConversationID = "conv-1"
		if parent != "" {
			m.InReplyTo = "<" + parent + "@x>"
		}
		return m
	}
	return []mail.Message{
		thread(msg("q1", "ann@example.org", 0, "Can we meet?"), ""),
		thread(sentMsg("a1", "ann@example.org", 2, "Sure — which day suits you?"), "q1"),
		thread(msg("q2", "ann@example.org", 30, "Thursday."), "a1"),
		thread(msg("q3", "ann@example.org", 50, "Any news?"), "q2"),
		thread(msg("q4", "ann@example.org", 74, "Ping"), "q3"),
	}
}

func TestThreadSignals(t *testing.T) {
	w := newWorld(t, everything, nil)
	w.write(conversation()...)
	w.run()
	cases := map[string]map[string]Value{
		"q1": {"thread_position": Int(0), "thread_last_sender": Str("none"), "owner_asked": Str("none"), "sender_reminders": Int(0),
			"thread_gap_minutes": NotApplicable(ReasonFirst), "back_and_forth": Int(0), "owner_replied_after": Bool(true)},
		"a1": {"thread_position": Int(1), "thread_last_sender": Str("other"), "sender_reminders": NotApplicable(ReasonSent),
			"back_and_forth": Int(1), "owner_replied_after": NotApplicable(ReasonSent), "direction": Str("sent_by_owner")},
		"q2": {"thread_position": Int(2), "thread_last_sender": Str("owner"), "owner_asked": Str("asked"), "sender_reminders": Int(0),
			"thread_gap_minutes": Num(28 * 60), "back_and_forth": Int(2), "owner_replied_after": Bool(false)},
		"q4": {"thread_position": Int(4), "thread_last_sender": Str("same_sender"), "owner_asked": Str("asked"), "sender_reminders": Int(2),
			"back_and_forth": Int(2), "is_reply": Bool(true), "direction": Str("received")},
	}
	for id, vals := range cases {
		r := w.record(id)
		for name, v := range vals {
			want(t, r, name, v)
		}
	}
}

func TestHistorySignals(t *testing.T) {
	w := newWorld(t, everything, nil)
	w.write(conversation()...)
	w.write(msg("cold", "zoe@new.example.net", 80, "Hello"), msg("colleague", "luc@example.com", 81, "Hi"),
		msg("sameorg", "eve@example.org", 82, "Hi"))
	w.run()
	q1, q2, q4 := w.record("q1"), w.record("q2"), w.record("q4")
	want(t, q1, "first_time_sender", Str("new_address_new_org"))
	want(t, q1, "sender_received_before", Int(0))
	want(t, q1, "owner_share", NotApplicable(ReasonFirst))
	want(t, q2, "first_time_sender", Str("known"))
	want(t, q2, "sender_received_before", Int(1))
	want(t, q2, "owner_sent_before", Int(1))
	want(t, q2, "owner_share", Num(0.5))
	want(t, q2, "reply_rate", Num(1))
	want(t, q2, "reply_latency_band", Int(1)) // 2 h: the 1h-4h band of the default buckets
	want(t, q2, "sender_first_seen_days", Num(30.0/24))
	want(t, q2, "exchange_years", Int(1))
	want(t, q4, "sender_received_before", Int(3))
	want(t, q4, "reply_rate", Num(1.0/3))
	want(t, w.record("sameorg"), "first_time_sender", Str("new_address"))
	want(t, w.record("cold"), "sender_relation", Str("external"))
	want(t, w.record("colleague"), "sender_relation", Str("internal")) // the owner's organisation (mailtest.Owner's domain)
	want(t, w.record("a1"), "sender_relation", Str("self"))
}

// Causality: mail that arrives later (and the owner's later replies)
// never changes the evidence of an earlier message — except the one
// signal declared not causal.
func TestLaterMailNeverChangesCausalEvidence(t *testing.T) {
	w := newWorld(t, everything, nil)
	first := conversation()[:1]
	w.write(first...)
	w.run()
	before := w.record("q1")
	w.write(conversation()[1:]...)
	w.write(msg("late", "ann@example.org", 200, "Again?"), msg("dup", "examp1e.org@x.example", 201, "x"))
	w.run()
	after := w.record("q1")
	for _, s := range Signals() {
		if s.Causal && !before[s.Name].Equal(after[s.Name]) {
			t.Errorf("%s changed: %+v -> %+v", s.Name, before[s.Name], after[s.Name])
		}
	}
	if !before["owner_replied_after"].Equal(Bool(false)) || !after["owner_replied_after"].Equal(Bool(true)) {
		t.Error("the non-causal outcome follows the later reply")
	}
}

func TestLookalikeDomains(t *testing.T) {
	w := newWorld(t, everything, nil)
	w.write(sentMsg("s1", "ann@paypal.example", 10, "Hello"),
		msg("early", "x@paypa1.example", 5, "Pay"),  // before the owner wrote to paypal.example: no correspondent yet
		msg("edit", "x@paypol.example", 20, "Pay"),  // one substitution
		msg("skel", "x@раураl.example", 21, "Pay"),  // Cyrillic letters: the same skeleton
		msg("same", "ann@paypal.example", 22, "Hi"), // the correspondent itself
		msg("own", "eve@exarnple.com", 23, "Hi"),    // the owner's own organisation (rn for m: a skeleton)
		msg("short", "x@ab.example", 24, "Hi"),      // too short to compare
		msg("far", "x@unrelated.example", 25, "Hi"))
	w.write(sentMsg("s2", "bob@ac.example", 11, "Hello"))
	w.run()
	for id, v := range map[string]string{"early": LookNone, "edit": LookEdit, "skel": LookSkeleton, "same": LookNone,
		"own": LookSkeleton, "short": LookNone, "far": LookNone} {
		want(t, w.record(id), "lookalike_domain", Str(v))
	}
	if mailtest.Owner == "" {
		t.Fatal("the owner's domain is the example.com of mailtest")
	}
}
