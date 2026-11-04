package threads

import (
	"slices"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

func TestHistoryIsCausal(t *testing.T) {
	c := func(m Message, conv string) Message { m.ConversationID = conv; return m }
	msgs := []Message{
		c(in(1, "a@y.com", "q", 0, "me@x.com"), "A"),
		c(out(2, "q", 30*time.Minute, "a@y.com"), "A"), // reply after 30 min
		c(in(3, "a@y.com", "q", 2*time.Hour, "me@x.com"), "B"),
		c(out(4, "q", 10*time.Hour, "a@y.com"), "B"), // reply after 8 h, at t0+10h
		c(in(5, "b@y.com", "q", 3*time.Hour, "me@x.com"), "C"),
	}
	h := Analyze(msgs, norm(t)).KnownHistory([]float64{60, 240, 1440}, 3, time.UTC, mail.NewDomains(nil))
	if _, ok := h.Sender("a@y.com", t0); ok {
		t.Fatal("nothing is known before the first message (it never sees itself)")
	}
	p, _ := h.Sender("a@y.com", t0.Add(2*time.Hour)) // message 3's own instant
	if p.Received != 1 || p.Replied != 1 || p.Bucket != 0 || len(p.Examples) != 1 || p.Examples[0].MessageID != 1 {
		t.Fatalf("at message 3: %+v", p)
	}
	p, _ = h.Sender("A@Y.com", t0.Add(9*time.Hour)) // message 3 not answered yet at 9 h
	if p.Received != 2 || p.Replied != 1 {
		t.Fatalf("a reply counts only from its own time on: %+v", p)
	}
	p, _ = h.Sender("a@y.com", t0.Add(11*time.Hour))
	if p.Replied != 2 || p.Bucket != 2 || p.Examples[0].MessageID != 3 {
		t.Fatalf("the median of 30 min and 8 h is their average, 255 min (4h-1d bucket): %+v", p)
	}
	if d, _ := h.Domain("someone@y.com", t0.Add(4*time.Hour)); d.Received != 3 || d.Replied != 1 {
		t.Fatalf("domain %+v", d)
	}
	if o, ok := h.Overall(t0.Add(time.Minute)); !ok || o.Received != 1 || o.Replied != 0 || o.Bucket != -1 {
		t.Fatalf("overall %+v", o)
	}
	if _, ok := h.Sender("", t0.Add(time.Hour)); ok {
		t.Fatal("empty sender")
	}
}

// Regression (review L0, bugs #2): conversations grow in time order. A
// later message bridging two conversations joins them only from its own
// instant on: what an earlier message saw (EarlierMembers) and how an
// earlier reply was paired (KnownReplies, KnownHistory) never change,
// while the final conversations (Threads, MyReplies) merge.
func TestConversationsGrowInTimeOrder(t *testing.T) {
	a := in(1, "ann@y.com", "A", 0, "me@x.com")
	a.SelfID = "<a>"
	b := in(2, "bob@z.com", "B", time.Hour, "me@x.com")
	b.SelfID = "<b>"
	r := out(3, "Re: B", 2*time.Hour, "carol@w.com") // addresses neither: paired only when one sender waits
	r.Parents = []string{"<b>"}
	bridge := in(4, "ann@y.com", "Both", 3*time.Hour, "me@x.com")
	bridge.Parents = []string{"<a>", "<b>"}
	undated := in(5, "ann@y.com", "Late", 0)
	undated.At, undated.Parents = time.Time{}, []string{"<a>"}
	an := Analyze([]Message{bridge, r, b, a, undated}, norm(t))
	if len(an.Threads) != 1 {
		t.Fatalf("the final conversation holds all: %d threads", len(an.Threads))
	}
	rows := func(ms []*Message) []int64 {
		var out []int64
		for _, m := range ms {
			out = append(out, m.RowID)
		}
		return out
	}
	for row, want := range map[int64][]int64{1: nil, 2: nil, 3: {2}, 4: {1, 2, 3}, 5: {1, 2, 3, 4}} {
		_, got, ok := an.EarlierMembers(row)
		if !ok || !slices.Equal(rows(got), want) {
			t.Errorf("EarlierMembers(%d) = %v, want %v", row, rows(got), want)
		}
	}
	if _, _, ok := an.EarlierMembers(99); ok {
		t.Error("an unknown row has no members")
	}
	if k, ok := an.KnownReplies[2]; !ok || k.ReplyRow != 3 {
		t.Fatalf("at the reply's instant only bob waited in its conversation: %+v", an.KnownReplies)
	}
	if _, ok := an.MyReplies[2]; ok {
		t.Fatal("over the final conversation two senders wait: no pairing (the retrospective view)")
	}
	h := an.KnownHistory([]float64{60, 240}, 0, time.UTC, mail.NewDomains(nil))
	if p, _ := h.Sender("bob@z.com", t0.Add(4*time.Hour)); p.Replied != 1 {
		t.Fatalf("the known pairing feeds the causal history: %+v", p)
	}
	x := h.AddressExchange("carol@w.com", t0.Add(4*time.Hour))
	if x.Sent != 1 || x.Received != 0 || !x.First.Equal(t0.Add(2*time.Hour)) || x.Years != 0 {
		t.Fatalf("exchange with carol: %+v", x)
	}
	if y := h.AddressExchange("bob@z.com", t0.Add(2*time.Hour)); y.Received != 1 || y.Sent != 0 {
		t.Fatalf("nothing at or after the instant counts: %+v", y)
	}
}

// Review of the crash-safety round, #11 (failed first: received 2,
// replied 2): the causal view holds every folder of the owner's mail, so
// one message filed twice (a copy in another folder: same store, same
// Message-ID) waited twice for an answer, and a copy of the owner's reply
// answered the second copy — the standing counted one exchange twice.
// Copies are one message: the first in time order stands for all; a copy
// shares its replies.
func TestCopiesOfAMessageCountOnce(t *testing.T) {
	q := in(1, "ann@y.com", "Budget", 0, "me@x.com")
	q.SelfID = "<q>"
	qCopy := q
	qCopy.RowID = 2
	r := out(3, "Re: Budget", time.Hour, "ann@y.com")
	r.SelfID, r.Parents = "<r>", []string{"<q>"}
	rCopy := r
	rCopy.RowID = 4
	other := q
	other.RowID, other.Store = 5, "another-store" // the same Message-ID in another mailbox is another message
	an := Analyze([]Message{q, qCopy, r, rCopy, other}, norm(t))
	h := an.KnownHistory([]float64{60, 240, 1440}, 3, time.UTC, mail.NewDomains(nil))
	p, _ := h.Sender("ann@y.com", t0.Add(2*time.Hour))
	if p.Received != 2 || p.Replied != 1 {
		t.Fatalf("one exchange per store: received %d replied %d, want 2 (one per mailbox) and 1", p.Received, p.Replied)
	}
	if an.KnownReplies[1].ReplyRow != 3 || an.KnownReplies[2] != an.KnownReplies[1] {
		t.Fatalf("a copy shares its replies: %+v / %+v", an.KnownReplies[1], an.KnownReplies[2])
	}
	if an.MyReplies[2] != an.MyReplies[1] || an.MyReplies[1].ReplyRow != 3 {
		t.Fatalf("final view: %+v / %+v", an.MyReplies[1], an.MyReplies[2])
	}
	if _, ok := an.KnownReplies[5]; ok {
		t.Fatal("the other mailbox's message was not answered there")
	}
	prof, err := an.Behaviour(Options{Zone: time.UTC, BucketMinutes: []int{60, 240}, ExamplesPerSender: 3})
	if err != nil || prof.Sent != 1 || prof.Totals.Received != 2 || prof.MatchedSent != 1 {
		t.Fatalf("the behaviour profile counts copies once: %v sent %d received %d matched %d", err, prof.Sent, prof.Totals.Received, prof.MatchedSent)
	}
}
