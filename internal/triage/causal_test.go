package triage

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/store"
)

func changed(before, after map[int64]string) (n int) {
	for id, s := range before {
		if StateHash(after[id]) != StateHash(s) {
			n++
		}
	}
	return n
}

// Review 2a #1 (repros R1/R1b, failed first: one label changed 39/40 and
// 19/60 states): a label never changes an existing state; only mail of the
// same sender or domain arriving after it quotes it.
func TestALabelNeverChangesExistingStates(t *testing.T) {
	w := newWorld(t, nil)
	var ids []int64
	subjects := []string{"Meeting tomorrow", "Invoice for March", "Your order shipped", "Lunch on Friday", "Re: the plan for march"}
	for i := range 40 {
		sender := fmt.Sprintf("p%d@gmail.com", i)
		if i%2 == 1 {
			sender = fmt.Sprintf("p@d%d.com", i)
		}
		ids = append(ids, w.mail(sender, fmt.Sprint("C", i), subjects[i%len(subjects)], time.Duration(i)*time.Hour, 0))
	}
	w.run(Options{})
	before := w.states()
	if err := w.st.PutLabel(t.Context(), ids[1], "category", `"work"`, store.LabelManual); err != nil {
		t.Fatal(err)
	}
	if err := w.st.PutLabel(t.Context(), ids[0], "reply_needed", "1", store.LabelManual); err != nil {
		t.Fatal(err)
	}
	if n := changed(before, w.states()); n != 0 {
		t.Fatalf("labels changed %d existing states", n)
	}
	if plan, _ := w.tr.Plan(t.Context(), Options{}); plan.Invalidated != 0 || plan.Pending != 0 {
		t.Fatalf("nothing to re-bill: %+v", plan)
	}
	// Mail arriving after the label, from another gmail.com sender, quotes it.
	later := w.mail("someone@gmail.com", "L", "Lunch", w.sinceNow(), 0)
	if s := w.states()[later]; !strings.Contains(s, `reply_needed=1.00`) {
		t.Fatalf("later mail of the domain learns from the label:\n%s", s)
	}
}

// Review 2a #4 (repro R2, failed first: the state flipped to "You almost
// always reply…" after the owner's reply): the message's own outcome is
// never in its state, and a reply changes no state of mail that arrived
// before it; only mail arriving after the reply knows it.
func TestARepliesNeverChangeExistingStates(t *testing.T) {
	w := newWorld(t, nil)
	in := w.mail("solo@vendor.com", "S1", "Question", 0, 0)
	w.mail("other@vendor.com", "S2", "Other", 10*time.Minute, 0) // before the reply below
	before := w.states()
	out := w.message("out-solo", mail.RoleSent, "Sent", me, "S1", "RE: Question", base.Add(20*time.Minute), "solo@vendor.com")
	w.write(out)
	after := w.states()
	if n := changed(before, after); n != 0 {
		t.Fatalf("a reply changed %d existing states:\n%s\n---\n%s", n, before[in], after[in])
	}
	next := w.mail("solo@vendor.com", "S3", "Again", 2*time.Hour, 0)
	if s := w.states()[next]; !strings.Contains(s, "You almost always reply to this sender (1-2 previous messages), typically within <1h.") {
		t.Fatalf("the next mail of the sender sees the reply:\n%s", s)
	}
}

// Review 2a #5a (repros R3/R3b, failed first: the 4th mail changed 2/2
// and 3/3 states): new mail of a sender crosses volume bands without
// changing any existing state.
func TestNewMailNeverChangesExistingStates(t *testing.T) {
	w := newWorld(t, nil)
	var ids []int64
	for i := range 3 {
		ids = append(ids, w.mail("v@vendor.com", fmt.Sprint("V", i), "Promo", time.Duration(i)*time.Hour, 0))
	}
	ids = append(ids, w.mail("other@o.com", "O", "x", 0, time.Minute))
	before := w.states()
	for i := range 12 { // crosses the 3, 10 volume bands and moves the overall rate
		w.mail("v@vendor.com", fmt.Sprint("W", i), "Promo", time.Duration(10+i)*time.Hour, 0)
		w.mail(fmt.Sprintf("n%d@new.org", i), fmt.Sprint("N", i), "Hi", time.Duration(10+i)*time.Hour, time.Minute)
	}
	after := w.states()
	if n := changed(before, after); n != 0 {
		t.Fatalf("new mail changed %d existing states", n)
	}
	if !strings.Contains(after[ids[2]], "You rarely reply to this sender (1-2 previous messages).") {
		t.Fatalf("the third mail knew only the two before it:\n%s", after[ids[2]])
	}
}

// Review 2a #5b (repro R4, failed first: 5/5 states changed): time.zone
// (a presentation setting) never changes a state. L0 #6: the arrival is
// in the sender's own Date-header offset (a Tokyo morning stays a
// morning), and omitted for mail without a Date header.
func TestTimeZoneNeverChangesStates(t *testing.T) {
	w := newWorld(t, nil)
	for i := range 5 {
		w.mail(fmt.Sprintf("z%d@x.org", i), fmt.Sprint("Z", i), "Hi", time.Duration(i)*time.Hour, 0)
	}
	dated := w.message("dated", mail.RoleInbox, "Inbox", "sato@example.jp", "J", "会議", base, me)
	dated.Headers = "Date: Mon, 2 Mar 2026 22:59:00 +0900"
	w.write(dated)
	before := w.states()
	w.withSettings(map[string]any{"time.zone": "Europe/Paris"})
	after := w.states()
	if n := changed(before, after); n != 0 {
		t.Fatalf("time.zone changed %d states", n)
	}
	sender := 0
	for _, s := range after {
		switch {
		case strings.Contains(s, "Arrived Monday at 23:00 in the sender's time (UTC+09:00)."):
			sender++
		case strings.Contains(s, "Arrived"):
			t.Fatalf("no Date header, no arrival fact:\n%s", s)
		}
	}
	if sender != 1 {
		t.Fatalf("the dated message shows its sender's time: %d", sender)
	}
}

// sinceNow is an offset from base that lies after now (labels are dated
// by the store's clock when they are made).
func (w *world) sinceNow() time.Duration { return time.Since(base) + time.Hour }

// Review L0 (bugs #1, rules #3), lead decision 2026-10-15: triage reads
// the causal view only, so the owner's later filing of a message (Deleted
// Items) never changes the state of another message.
func TestFilingNeverChangesAnotherState(t *testing.T) {
	w := newWorld(t, nil)
	first := w.mail("ann@vendor.com", "C1", "Question", 0, 30*time.Minute)
	later := w.mail("ann@vendor.com", "C2", "Again", 5*time.Hour, 0)
	before := w.states()[later]
	var moved mail.Message
	_ = w.st.EachMessage(t.Context(), store.MessageFilter{RowIDs: []int64{first}}, func(r *store.MessageRow) error {
		moved = r.Message
		return nil
	})
	moved.FolderPath, moved.FolderRole = "Deleted Items", mail.RoleDeleted
	w.write(moved)
	if after := w.states()[later]; before != after {
		t.Errorf("a later filing changed another state:\n%s\n---\n%s", before, after)
	}
}
