package evidence

import "github.com/LLM-4-People/mailsense/internal/mail"

func init() {
	var roles []string
	for _, r := range mail.Roles() {
		roles = append(roles, string(r))
	}
	register(Signal{Summary: true, Name: "folder_role", Version: 1, Type: TypeEnum, Values: roles, Needs: []string{"folder_roles"}, Group: GroupOwner,
		Doc:     "The role of the folder the message is filed in (the source's role, never a name). NOT causal: filing is the owner's later action.",
		Extract: func(in *Input) Value { return Str(string(in.Msg.FolderRole)) }})
}
