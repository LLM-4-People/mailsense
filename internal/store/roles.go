package store

import (
	"strings"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// rolesSQL selects the messages (m, main folder f) that belong to a set of
// folder roles, over EVERY folder of the message (its main folder and its
// further folders, such as Gmail labels): at least one folder has a listed
// role, and no folder has an unlisted role other than "other" (the role of
// plain folders and labels). So a Gmail message labelled Spam or Trash is
// out of a [inbox sent archive other] selection whatever its other labels,
// and an "All mail" message labelled Inbox is in an [inbox] one. It is the
// one role rule of every filter (search, triage and learners, the index).
func rolesSQL(roles []mail.FolderRole) (string, []any) {
	list := placeholders(len(roles))
	args := stringArgs(roles)
	other := string(mail.RoleOther)
	further := "SELECT 1 FROM message_folders mf JOIN folders fx ON fx.id = mf.folder_id WHERE mf.message_id = m.id AND "
	conds := []string{
		"(f.role IN (" + list + ") OR EXISTS (" + further + "fx.role IN (" + list + ")))",
		"(f.role IN (" + list + ") OR f.role = ?)",
		"NOT EXISTS (" + further + "fx.role NOT IN (" + list + ") AND fx.role <> ?)",
	}
	var out []any
	for _, part := range [][]any{args, args, args, {other}, args, {other}} {
		out = append(out, part...)
	}
	return "(" + strings.Join(conds, " AND ") + ")", out
}
