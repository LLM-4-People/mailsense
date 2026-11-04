package evidence

import (
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/threads"
)

// threadView places every threaded message in its conversation as it
// stood at the message's own instant (threads.Analysis.EarlierMembers).
type threadView struct {
	an    *threads.Analysis
	asked map[int64]bool // the owner's messages whose own text asks a question
}

func newThreadView(an *threads.Analysis, asked map[int64]bool) *threadView {
	return &threadView{an: an, asked: asked}
}

// earlier returns the message and the members of its conversation before
// it, as known at its instant (most recent last); ok is false for a
// message outside the threads, and the reason says why.
func (v *threadView) earlier(row int64) (*threads.Message, []*threads.Message, string, bool) {
	self, out, ok := v.an.EarlierMembers(row)
	switch {
	case !ok:
		return nil, nil, ReasonNotThread, false
	case !self.Dated():
		return nil, nil, ReasonNoDate, false
	}
	return self, out, "", true
}

// thread answers a thread signal: NA outside the threads, else fn.
func thread(in *Input, fn func(self *threads.Message, earlier []*threads.Message) Value) Value {
	in.uses(DataThreads)
	self, earlier, reason, ok := in.env.thread.earlier(in.Row)
	if !ok {
		return NotApplicable(reason)
	}
	return fn(self, earlier)
}

func sent(m *threads.Message) bool { return m.Role == mail.RoleSent }
