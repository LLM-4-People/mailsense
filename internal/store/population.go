package store

import (
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/mail"
)

// SettingTriageFolderRoles is the setting that names the triaged population's
// folder roles. The store reads it for the route-answer and review scopes;
// triage and layer 1 read the same population through it. It lives here
// because the store is the lowest package all three import: triage imports the
// store, so the store cannot take the name from triage.
const SettingTriageFolderRoles = "triage.folder_roles"

// TriageFolderRoles converts the setting to the folder-role vocabulary. One
// conversion serves the store's own scopes and every reader above it, so a
// role cannot be spelled differently in two places.
func TriageFolderRoles(cfg *config.Config) []mail.FolderRole {
	values := cfg.Strings(SettingTriageFolderRoles)
	out := make([]mail.FolderRole, len(values))
	for i, value := range values {
		out[i] = mail.FolderRole(value)
	}
	return out
}
