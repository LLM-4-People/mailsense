package evidence

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// Regression (review L0, bugs #1, rules #3): where the owner files a
// message later (Deleted Items, Junk) is the owner's later action; the
// causal history of OTHER messages never depends on it.
func TestOwnerFilingNeverChangesCausalHistory(t *testing.T) {
	w := newWorld(t, everything, nil)
	a1 := msg("a1", "ann@example.org", 0, "First")
	reply := sentMsg("r1", "ann@example.org", 1, "Answer")
	reply.InReplyTo, a1.ConversationID, reply.ConversationID = "<a1@x>", "c1", "c1"
	a2 := msg("a2", "ann@example.org", 5, "Second")
	w.write(a1, reply, a2)
	w.run()
	before := w.record("a2")
	for _, role := range []mail.FolderRole{mail.RoleDeleted, mail.RoleJunk} {
		moved := a1
		moved.FolderPath, moved.FolderRole = string(role), role
		w.write(moved)
		w.run()
		after := w.record("a2")
		for _, s := range Signals() {
			if s.Causal && !before[s.Name].Equal(after[s.Name]) {
				t.Errorf("%s: a1 filed in %s changed a2's %s: %+v -> %+v", role, role, s.Name, before[s.Name], after[s.Name])
			}
		}
	}
	want(t, before, "sender_received_before", Int(1))
	want(t, before, "first_time_sender", Str("known"))
	want(t, before, "reply_rate", Num(1))
	// A draft is no mail that happened: it is never part of the history.
	draft := sentMsg("d1", "zed@example.net", 2, "unsent")
	draft.FolderPath, draft.FolderRole = "Drafts", mail.RoleDrafts
	w.write(draft, msg("z1", "zed@example.net", 6, "Hello"))
	w.run()
	want(t, w.record("z1"), "owner_sent_before", Int(0))
}

// Regression (review L0, bugs #2, rules #2): a later message whose
// References name two earlier, unrelated conversations merges them only
// from its own instant on; the earlier messages keep what they saw.
func TestALaterBridgeNeverChangesEarlierThreads(t *testing.T) {
	w := newWorld(t, everything, nil)
	a := msg("a", "ann@example.org", 0, "Topic A")
	b := msg("b", "bob@example.net", 1, "Topic B")
	ownerB := sentMsg("ob", "bob@example.net", 2, "Answer B?")
	ownerB.InReplyTo = "<b@x>"
	w.write(a, b, ownerB)
	w.run()
	before := map[string]Record{"a": w.record("a"), "b": w.record("b"), "ob": w.record("ob")}
	bridge := msg("bridge", "ann@example.org", 10, "Both")
	bridge.References = "<a@x> <b@x>"
	w.write(bridge)
	w.run()
	for id, rec := range before {
		after := w.record(id)
		for _, s := range Signals() {
			if s.Causal && !rec[s.Name].Equal(after[s.Name]) {
				t.Errorf("%s: %s changed after the bridge: %+v -> %+v", id, s.Name, rec[s.Name], after[s.Name])
			}
		}
	}
	want(t, before["b"], "thread_position", Int(0))
	want(t, before["b"], "thread_last_sender", Str("none"))
	r := w.record("bridge")
	want(t, r, "thread_position", Int(3)) // from its own instant on, the bridge sees both conversations
	want(t, r, "owner_asked", Str("asked"))
}
