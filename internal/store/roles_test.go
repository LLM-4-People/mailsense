package store

import (
	"slices"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
)

// Review L0 bugs #11 (failed first: the Spam- and Trash-labelled messages
// were triaged, the Inbox-labelled one was not in an inbox selection):
// roles are judged over every folder of a message.
func TestRolesCoverEveryFolderOfAMessage(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	label := func(id string, refs ...mail.FolderRef) mail.Message {
		m := mailtest.Message("src", id)
		m.FolderPath, m.FolderRole, m.Folders = "All mail", mail.RoleOther, refs
		return m
	}
	msgs := []mail.Message{
		label("plain"),
		label("inbox", mail.FolderRef{Path: "Inbox", Role: mail.RoleInbox}, mail.FolderRef{Path: "Work", Role: mail.RoleOther}),
		label("spam", mail.FolderRef{Path: "Spam", Role: mail.RoleJunk}),
		label("trash", mail.FolderRef{Path: "Inbox", Role: mail.RoleInbox}, mail.FolderRef{Path: "Trash", Role: mail.RoleDeleted}),
	}
	if err := s.UpsertMessages(ctx, 0, msgs); err != nil {
		t.Fatal(err)
	}
	ids := func(roles ...mail.FolderRole) []string {
		var out []string
		_ = s.EachMessage(ctx, MessageFilter{Roles: roles}, func(r *MessageRow) error { out = append(out, r.Message.ID); return nil })
		slices.Sort(out)
		return out
	}
	triaged := []mail.FolderRole{mail.RoleInbox, mail.RoleSent, mail.RoleArchive, mail.RoleOther}
	if got := ids(triaged...); !slices.Equal(got, []string{"inbox", "plain"}) {
		t.Errorf("triage roles: %v", got)
	}
	if got := ids(mail.RoleInbox); !slices.Equal(got, []string{"inbox"}) {
		t.Errorf("inbox only: %v", got)
	}
	if got := ids(mail.RoleJunk); !slices.Equal(got, []string{"spam"}) {
		t.Errorf("junk only: %v", got)
	}
	// The same rule filters search and the vectors.
	all, _ := s.FilterIDs(ctx, SearchFilter{Roles: triaged})
	states, _ := s.EmbeddingStates(ctx, triaged)
	if len(all) != 2 || len(states) != 2 {
		t.Errorf("search %d, index %d", len(all), len(states))
	}
}
