package threads

import (
	"cmp"
	"slices"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// Reply pairs a message with the message that answered it.
type Reply struct {
	ReplyRow int64   `json:"reply_row"` // the answering message
	Seconds  float64 `json:"seconds"`   // time from the message to its answer (≥ 0)
	Excerpt  string  `json:"excerpt"`   // the answer's opening
	From     string  `json:"from"`      // the answer's sender
}

// Thread is one conversation, members in time order (undated last).
type Thread struct {
	ID         int64 `json:"id"` // smallest member row id
	Members    []int `json:"members"`
	LastIsMine bool  `json:"last_is_mine"` // the owner sent the last dated message
}

// ordered sorts thread members by time then row id (undated last).
func ordered(msgs []Message, members []int) []int {
	out := slices.Clone(members)
	slices.SortStableFunc(out, func(a, b int) int {
		ma, mb := &msgs[a], &msgs[b]
		switch {
		case ma.Dated() != mb.Dated():
			if ma.Dated() {
				return -1
			}
			return 1
		case !ma.At.Equal(mb.At):
			return ma.At.Compare(mb.At)
		}
		return cmp.Compare(ma.RowID, mb.RowID)
	})
	return out
}

// pairing is one direction of reply matching: messages of wait wait for
// an answer, every other message answers the most recent waiting one it
// accepts.
type pairing struct {
	wait    mail.FolderRole
	accepts func(answer, waiting *Message) bool
}

// mine pairs each owner reply with the most recent still-unanswered inbox
// message whose sender it addresses (no recipients: the most recent);
// failing that, the last one when all pending mail comes from a single
// address (history.match_replies).
var mine = pairing{wait: mail.RoleInbox, accepts: func(answer, waiting *Message) bool {
	return len(answer.Recipients) == 0 || answer.addressedTo(waiting.Sender)
}}

// theirs is the mirror image: an inbox message answers the most recent
// sent message whose sender it addresses (no empty-recipient rule), with
// the same single-counterpart fallback (threads.their_replies).
var theirs = pairing{wait: mail.RoleSent, accepts: func(answer, waiting *Message) bool { return answer.addressedTo(waiting.Sender) }}

// match walks a thread in time order with the pairing.
func (p pairing) match(msgs []Message, members []int, out map[int64]Reply) {
	var waiting []int
	for _, i := range members {
		waiting = p.step(msgs, waiting, i, out)
	}
}

// step handles message i against the waiting list of its conversation
// and returns the list after it.
func (p pairing) step(msgs []Message, waiting []int, i int, out map[int64]Reply) []int {
	m := &msgs[i]
	if !m.Dated() || !m.Kind.Ordinary() { // meeting responses, reports, auto-replies never reply or wait
		return waiting
	}
	if m.Role == p.wait {
		return append(waiting, i)
	}
	chosen := pick(msgs, waiting, func(w *Message) bool { return p.accepts(m, w) })
	if chosen < 0 {
		return waiting
	}
	answered := &msgs[waiting[chosen]]
	waiting = slices.Delete(waiting, chosen, chosen+1)
	if s := m.At.Sub(answered.At).Seconds(); s >= 0 {
		out[answered.RowID] = Reply{ReplyRow: m.RowID, Seconds: s, Excerpt: m.Excerpt, From: m.Sender}
	}
	return waiting
}

// pick returns the position in waiting of the most recent message match
// accepts, else the last one when every waiting message has one sender,
// else -1.
func pick(msgs []Message, waiting []int, match func(*Message) bool) int {
	for k := len(waiting) - 1; k >= 0; k-- {
		if match(&msgs[waiting[k]]) {
			return k
		}
	}
	senders := map[string]bool{}
	for _, w := range waiting {
		senders[msgs[w].Sender] = true
	}
	if len(senders) == 1 {
		return len(waiting) - 1
	}
	return -1
}
