package threads

import (
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

func norm(t *testing.T) *mail.SubjectNormalizer {
	n, err := mail.NewSubjectNormalizer(1, []string{"re", "fw", "tr"})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

var t0 = time.Date(2026, 1, 5, 14, 0, 0, 0, time.UTC)

func in(row int64, from, subject string, at time.Duration, to ...string) Message {
	return Message{RowID: row, Store: "s", Role: mail.RoleInbox, Sender: from, Domain: mail.NewDomains(nil).Of(from), Subject: subject, At: t0.Add(at), Recipients: to,
		Excerpt: "in " + subject}
}

func out(row int64, subject string, at time.Duration, to ...string) Message {
	return Message{RowID: row, Store: "s", Role: mail.RoleSent, Sender: "me@x.com", Domain: "x.com", Subject: subject, At: t0.Add(at), Recipients: to,
		Excerpt: "reply " + subject}
}

func threadIDs(a *Analysis, rows ...int64) map[int64]bool {
	ids := map[int64]bool{}
	for _, r := range rows {
		th, _ := a.ThreadOf(r)
		ids[th.ID] = true
	}
	return ids
}

func TestHeaderLinksWinAndEmptySubjectsStayApart(t *testing.T) {
	a := in(1, "a@y.com", "Budget", 0)
	a.SelfID = "<1@y>"
	b := out(2, "Totally different", time.Hour, "a@y.com")
	b.Parents = []string{"<1@y>"} // a reply whose subject was edited still joins by header
	c := in(3, "c@z.com", "", 0)
	d := in(4, "d@z.com", "", time.Minute) // legacy: c and d shared one "s:" thread
	e := in(5, "e@z.com", "Re: Lunch", 0)
	f := in(6, "f@z.com", "lunch", time.Minute)
	g := in(7, "g@z.com", "Lunch", 2*time.Minute)
	g.ConversationID = "CONV-G" // has evidence: never joins by subject
	other := in(8, "a@y.com", "Budget", 0)
	other.Store = "delegate" // another mailbox store: never the same thread
	other.SelfID = "<1@y>"
	an := Analyze([]Message{a, b, c, d, e, f, g, other}, norm(t))
	if len(threadIDs(an, 1, 2)) != 1 {
		t.Fatal("In-Reply-To joins a reply to its parent")
	}
	if len(threadIDs(an, 3, 4)) != 2 {
		t.Fatal("empty subjects must not be lumped into one thread (legacy flaw)")
	}
	if len(threadIDs(an, 5, 6)) != 1 || len(threadIDs(an, 5, 7)) != 2 || len(threadIDs(an, 1, 8)) != 2 {
		t.Fatal("subject fallback only without evidence, and only within one store")
	}
	if r, ok := an.MyReplies[1]; !ok || r.Seconds != 3600 || r.ReplyRow != 2 || r.Excerpt != "reply Totally different" {
		t.Fatalf("reply matched through the header thread: %+v", an.MyReplies)
	}
}

func TestConversationIndexHeaderJoins(t *testing.T) {
	idx := "01D9" + strings.Repeat("A", 40)
	a, b := in(1, "a@y.com", "x", 0), out(2, "y", time.Minute, "a@y.com")
	a.ConversationIndex, b.ConversationIndex = idx, idx+"0011223344"
	if an := Analyze([]Message{a, b}, norm(t)); len(threadIDs(an, 1, 2)) != 1 {
		t.Fatal("a child ConversationIndex shares the 22-byte header")
	}
}

func TestReplyMatchingBothDirectionsUsesSentOn(t *testing.T) {
	conv := func(m Message) Message { m.ConversationID = "C"; return m }
	msgs := []Message{
		conv(in(1, "a@y.com", "q", 0, "me@x.com")),
		conv(in(2, "b@y.com", "q", time.Minute, "me@x.com")),
		conv(out(3, "q", 30*time.Minute, "a@y.com")), // answers a (addressed), not the more recent b
		conv(in(4, "a@y.com", "q", 2*time.Hour, "me@x.com")),
		conv(out(5, "q", 3*time.Hour)), // no recipients: the most recent pending (4, then 2 remains)
	}
	an := Analyze(msgs, norm(t))
	if an.MyReplies[1].ReplyRow != 3 || an.MyReplies[4].ReplyRow != 5 || len(an.MyReplies) != 2 {
		t.Fatalf("my replies %+v", an.MyReplies)
	}
	if r := an.TheirReplies[3]; r.ReplyRow != 4 || r.Seconds != 90*60 || r.From != "a@y.com" {
		t.Fatalf("their replies %+v", an.TheirReplies)
	}
	if th, _ := an.ThreadOf(1); !th.LastIsMine {
		t.Fatal("the owner sent the last message")
	}
	if c, ok := an.Counterpart(5); !ok || c.RowID != 4 {
		t.Fatalf("counterpart %+v", c)
	}
	// FromMail: a sent item's time is its sent_on, not its received_at.
	m := &mail.Message{FolderRole: mail.RoleSent, ReceivedAt: "2026-01-05T09:00:00Z", SentOn: "2026-01-05T14:30:00Z", Body: " a\nb ",
		SenderAddress: " Me@X.com", Recipients: []mail.Recipient{{Kind: mail.To, Address: "A@y.com"}, {Kind: mail.Bcc, Address: "hidden@y.com"}}}
	v := FromMail(9, m, false, 10, mail.NewDomains(nil))
	if !v.At.Equal(t0.Add(30*time.Minute)) || v.Excerpt != "a b" || v.Words != 2 || v.Sender != "me@x.com" || len(v.Recipients) != 1 {
		t.Fatalf("FromMail %+v", v)
	}
}

func TestBehaviourIsComputedInTheOwnersZone(t *testing.T) {
	newYork, _ := time.LoadLocation("America/New_York")
	late := Message{RowID: 1, Store: "s", Role: mail.RoleInbox, Sender: "a@y.com", Domain: "y.com", Subject: "late", ConversationID: "L",
		At: time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)} // 22:00 on Dec 31 in New York
	reply := Message{RowID: 2, Store: "s", Role: mail.RoleSent, Sender: "me@x.com", ConversationID: "L", Recipients: []string{"a@y.com"},
		At: late.At.Add(90 * time.Minute), Excerpt: "ok"}
	quiet := in(3, "b@y.com", "nobody answers", 0)
	undated := Message{RowID: 4, Store: "s", Role: mail.RoleInbox, Sender: "b@y.com", Domain: "y.com", Subject: "ndr"}
	p, err := Analyze([]Message{late, reply, quiet, undated}, norm(t)).Behaviour(Options{Zone: newYork, BucketMinutes: []int{60, 240, 1440, 4320}, ExamplesPerSender: 3})
	if err != nil {
		t.Fatal(err)
	}
	if v := p.Months["2025-12"]; v.Received != 1 || v.Replied != 1 {
		t.Fatalf("months are local: %+v", p.Months)
	}
	if s := p.Senders["a@y.com"]; s.ReplyRate != 1 || *s.MedianMinutes != 90 || s.FirstSeen != "2025-12-31T22:00:00-05:00" {
		t.Fatalf("sender stats %+v", s)
	}
	if d := p.Domains["y.com"]; d.Received != 3 || d.Replied != 1 {
		t.Fatalf("domain stats count undated mail too: %+v", d)
	}
	labels := []string{"<1h", "1h-4h", "4h-1d", "1d-3d", ">3d", NoReply}
	for i, b := range p.Buckets {
		if b.Label != labels[i] {
			t.Fatalf("bucket labels %+v", p.Buckets)
		}
	}
	if p.Buckets[1].Count != 1 || p.Buckets[5].Count != 2 || p.Totals.Received != 3 || p.Sent != 1 || p.MatchedSent != 1 {
		t.Fatalf("buckets/totals %+v %+v", p.Buckets, p.Totals)
	}
	if ex := p.Examples["a@y.com"]; len(ex) != 1 || ex[0].RespondedMinutes != 90 || ex[0].ReplyPreview != "ok" || ex[0].MessageID != 1 {
		t.Fatalf("examples %+v", p.Examples)
	}
	if _, err := Analyze(nil, norm(t)).Behaviour(Options{Zone: newYork, BucketMinutes: []int{60, 60}}); err == nil {
		t.Fatal("non-ascending buckets are refused")
	}
	if _, err := Analyze(nil, norm(t)).Behaviour(Options{BucketMinutes: []int{60}}); err == nil {
		t.Fatal("a zone is required")
	}
}

func TestSpanAndBucketIndex(t *testing.T) {
	if Span(45) != "45m" || Span(240) != "4h" || Span(4320) != "3d" || Span(90) != "90m" {
		t.Fatal("span")
	}
	if BucketIndex(59.9, []float64{60, 240}) != 0 || BucketIndex(60, []float64{60, 240}) != 1 || BucketIndex(1e6, []float64{60, 240}) != 2 {
		t.Fatal("index")
	}
	if l := BucketLabels([]int{60, 240}); len(l) != 3 || l[2] != ">4h" {
		t.Fatal(l)
	}
}

// #4: a meeting response, an NDR or an automatic reply is never a reply
// and never waits for one.
func TestNonOrdinaryItemsStayOutOfReplyMatching(t *testing.T) {
	q := in(1, "a@y.com", "Planning", 0, "me@x.com")
	q.ConversationID = "M"
	accept := out(2, "Accepted: Planning", 10*time.Minute, "a@y.com")
	accept.ConversationID, accept.Kind = "M", mail.KindMeetingResponse
	ndr := in(3, "postmaster@y.com", "Undeliverable", 20*time.Minute, "me@x.com")
	ndr.ConversationID, ndr.Kind = "M", mail.KindDeliveryReport
	real := out(4, "RE: Planning", time.Hour, "a@y.com")
	real.ConversationID = "M"
	an := Analyze([]Message{q, accept, ndr, real}, norm(t))
	r, ok := an.MyReplies[1]
	if !ok || r.ReplyRow != 4 || len(an.MyReplies) != 1 {
		t.Fatalf("only the real reply answers: %+v", an.MyReplies)
	}
	if _, waits := an.MyReplies[3]; waits {
		t.Fatal("an NDR never waits for a reply")
	}
}
