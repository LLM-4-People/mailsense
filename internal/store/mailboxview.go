package store

import (
	"context"
	"slices"

	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// MailboxView is one mailbox as the picking and identity screen shows it,
// the one query of that screen (and of the learning stage and the
// learners): the store as its source's last discovery saw it (Addresses:
// what the source reported at its last complete read), whether it is
// picked, and its owner identity (reported + declared).
type MailboxView struct {
	source.Mailbox
	// Discovered is false for a store that is picked or has declared
	// addresses but was never seen by a discovery (only Key is known).
	Discovered bool           `json:"discovered"`
	Selected   bool           `json:"selected"`
	Declared   []string       `json:"declared,omitempty"`
	Identity   scope.Identity `json:"identity"`
}

// MailboxViews lists every discovered mailbox, then every picked or
// declared store key no discovery has seen (sorted by key).
func (s *Store) MailboxViews(ctx context.Context) ([]MailboxView, error) {
	boxes, err := s.mailboxes(ctx)
	if err != nil {
		return nil, err
	}
	owners, err := s.Owners(ctx)
	if err != nil {
		return nil, err
	}
	_, picked := s.selection()
	var out []MailboxView
	seen := map[string]bool{}
	view := func(b source.Mailbox, discovered bool) MailboxView {
		o := owners[b.Key]
		b.Addresses = o.Reported
		seen[b.Key] = true
		return MailboxView{Mailbox: b, Discovered: discovered, Selected: picked[b.Key], Declared: o.Declared, Identity: o.Identity}
	}
	for _, b := range boxes {
		out = append(out, view(b, true))
	}
	var missing []string
	for k := range picked {
		missing = append(missing, k)
	}
	for k := range owners {
		missing = append(missing, k)
	}
	slices.Sort(missing)
	for _, k := range slices.Compact(missing) {
		if !seen[k] {
			out = append(out, view(source.Mailbox{Key: k, Folders: -1, Items: -1}, false))
		}
	}
	return out, nil
}
