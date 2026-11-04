package threads

import "github.com/LLM-4-People/mailsense/internal/mail"

// Analysis is the threaded view of the owner's mail.
type Analysis struct {
	Messages []Message `json:"-"`
	Threads  []Thread  `json:"threads"`
	// MyReplies: inbox row id → the owner's reply to it, paired over the
	// final conversations (retrospective: analytics, outcomes).
	MyReplies map[int64]Reply `json:"my_replies"`
	// TheirReplies: sent row id → the correspondent's reply to it.
	TheirReplies map[int64]Reply `json:"their_replies"`
	// KnownReplies: inbox row id → the owner's reply to it, paired at the
	// reply's own instant over the conversation as it stood then (causal:
	// a later message bridging two conversations never re-pairs it).
	KnownReplies map[int64]Reply `json:"-"`
	threadOf     map[int64]int
	index        map[int64]int
	earlier      [][]int // message index → its conversation's members before it, as known at its instant
	// copyOf: message index → the index of the message it is a copy of
	// (same store and Message-ID, filed twice), -1 for the original.
	copyOf []int
}

// copies maps every message to the first message, in time order, with the
// same store and Message-ID (-1 for itself or without a Message-ID): the
// causal view holds every folder of the owner's mail, so a message filed
// twice is two rows of one message.
func copies(msgs []Message, order []int) []int {
	out := make([]int, len(msgs))
	first := map[[2]string]int{}
	for _, i := range order {
		out[i] = -1
		if id := msgs[i].SelfID; id != "" {
			k := [2]string{msgs[i].Store, id}
			if j, ok := first[k]; ok {
				out[i] = j
			} else {
				first[k] = i
			}
		}
	}
	return out
}

// originals is members without the copies (reply matching sees each
// message once).
func (a *Analysis) originals(members []int) []int {
	out := make([]int, 0, len(members))
	for _, i := range members {
		if a.copyOf[i] < 0 {
			out = append(out, i)
		}
	}
	return out
}

// shareReplies gives every copy its original's replies.
func (a *Analysis) shareReplies() {
	for i, j := range a.copyOf {
		if j < 0 {
			continue
		}
		for _, m := range []map[int64]Reply{a.KnownReplies, a.MyReplies, a.TheirReplies} {
			if r, ok := m[a.Messages[j].RowID]; ok {
				m[a.Messages[i].RowID] = r
			}
		}
	}
}

// Analyze threads msgs and matches replies in both directions. Messages
// join their conversations in time order (undated last), so each one
// keeps the members its conversation held at its own instant
// (EarlierMembers) while Threads are the final conversations. Copies of a
// message (same store and Message-ID) are one message: only the first in
// time order waits, answers and counts in the history; the copies share
// its replies.
func Analyze(msgs []Message, norm *mail.SubjectNormalizer) *Analysis {
	a := &Analysis{Messages: msgs, MyReplies: map[int64]Reply{}, TheirReplies: map[int64]Reply{}, KnownReplies: map[int64]Reply{},
		threadOf: map[int64]int{}, index: map[int64]int{}}
	all := make([]int, len(msgs))
	for i := range msgs {
		a.index[msgs[i].RowID] = i
		all[i] = i
	}
	g := newGrowth(len(msgs))
	order := ordered(msgs, all)
	a.copyOf = copies(msgs, order)
	for rank, i := range order {
		g.add(i, rank, keys(&msgs[i], norm))
		if a.copyOf[i] < 0 { // a copy neither waits nor answers: its original does
			r := g.find(i)
			g.waiting[r] = mine.step(msgs, g.waiting[r], i, a.KnownReplies)
		}
	}
	a.earlier = g.earlier
	for _, members := range g.components() {
		members = ordered(msgs, members)
		t := Thread{ID: msgs[members[0]].RowID, Members: members}
		for _, i := range members {
			t.ID = min(t.ID, msgs[i].RowID)
			if msgs[i].Dated() {
				t.LastIsMine = msgs[i].Role == mail.RoleSent
			}
			a.threadOf[msgs[i].RowID] = len(a.Threads)
		}
		mine.match(msgs, a.originals(members), a.MyReplies)
		theirs.match(msgs, a.originals(members), a.TheirReplies)
		a.Threads = append(a.Threads, t)
	}
	a.shareReplies()
	return a
}

// Message returns a message by row id.
func (a *Analysis) Message(rowID int64) (*Message, bool) {
	i, ok := a.index[rowID]
	if !ok {
		return nil, false
	}
	return &a.Messages[i], true
}

// EarlierMembers returns a message and the members of its conversation
// as it stood at the message's own instant: the messages joined to it by
// then (dated before it, or at the same instant with a smaller row id),
// in time order, most recent last. A message that arrives later — even
// one bridging two conversations — never changes them. ok is false for
// a row outside the analysis.
func (a *Analysis) EarlierMembers(rowID int64) (*Message, []*Message, bool) {
	i, ok := a.index[rowID]
	if !ok {
		return nil, nil, false
	}
	out := make([]*Message, 0, len(a.earlier[i]))
	for _, j := range a.earlier[i] {
		if a.Messages[j].Dated() {
			out = append(out, &a.Messages[j])
		}
	}
	return &a.Messages[i], out, true
}

// ThreadOf returns the thread of a message.
func (a *Analysis) ThreadOf(rowID int64) (*Thread, bool) {
	i, ok := a.threadOf[rowID]
	if !ok {
		return nil, false
	}
	return &a.Threads[i], true
}

// Counterpart is the most recent inbox message of the thread before a sent
// message (the message the owner was answering), indexed by thread.
func (a *Analysis) Counterpart(sentRow int64) (*Message, bool) {
	sent, ok := a.Message(sentRow)
	t, ok2 := a.ThreadOf(sentRow)
	if !ok || !ok2 || !sent.Dated() {
		return nil, false
	}
	var best *Message
	for _, i := range t.Members {
		m := &a.Messages[i]
		if m.Role == mail.RoleInbox && m.Dated() && m.At.Before(sent.At) && (best == nil || m.At.After(best.At)) {
			best = m
		}
	}
	return best, best != nil
}
