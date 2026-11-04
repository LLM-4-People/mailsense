package threads

import (
	"slices"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// conversationIndexHeader is the hex length of a ConversationIndex header
// block (22 bytes): every message of one conversation shares it.
const conversationIndexHeader = 44

// keys are the evidence nodes a message joins. Evidence, in order of
// strength: Internet Message-ID links (a reply references its parent, so
// even mail whose parent is missing joins its siblings), then
// ConversationID or the ConversationIndex header. A message with none of
// these joins others of the same normalized subject — never when that
// subject is empty (the legacy flaw lumped all of them into one thread).
func keys(m *Message, norm *mail.SubjectNormalizer) []string {
	var out []string
	evidence := false
	if m.SelfID != "" {
		out = append(out, "id\x00"+m.Store+m.SelfID)
	}
	for _, p := range m.Parents {
		out = append(out, "id\x00"+m.Store+p)
		evidence = true
	}
	switch {
	case m.ConversationID != "":
		out = append(out, "c\x00"+m.Store+m.ConversationID)
		evidence = true
	case len(m.ConversationIndex) >= conversationIndexHeader:
		out = append(out, "x\x00"+m.Store+m.ConversationIndex[:conversationIndexHeader])
		evidence = true
	}
	if !evidence {
		if s := norm.Normalize(m.Subject); s != "" {
			out = append(out, "s\x00"+m.Store+s)
		}
	}
	return out
}

// union is a union-find over message indices plus named evidence nodes.
type union struct {
	parent []int
	named  map[string]int
	n      int
}

func newUnion(n int) *union {
	u := &union{parent: make([]int, n), named: map[string]int{}, n: n}
	for i := range u.parent {
		u.parent[i] = i
	}
	return u
}

func (u *union) node(name string) int {
	if id, ok := u.named[name]; ok {
		return id
	}
	id := len(u.parent)
	u.parent = append(u.parent, id)
	u.named[name] = id
	return id
}

func (u *union) find(i int) int {
	for u.parent[i] != i {
		u.parent[i] = u.parent[u.parent[i]]
		i = u.parent[i]
	}
	return i
}

// components lists the message members of every component, ordered by
// their first member.
func (u *union) components() [][]int {
	byRoot := map[int][]int{}
	var roots []int
	for i := range u.n {
		r := u.find(i)
		if _, ok := byRoot[r]; !ok {
			roots = append(roots, r)
		}
		byRoot[r] = append(byRoot[r], i)
	}
	out := make([][]int, 0, len(roots))
	for _, r := range roots {
		out = append(out, slices.Clone(byRoot[r]))
	}
	return out
}
