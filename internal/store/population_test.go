package store

import (
	"slices"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/mail"
)

// TestTriageFolderRolesIsTheSchemaSetting pins the one spelling of the triaged
// population's setting: the constant the store scopes with must be a real
// schema key, and the conversion must yield exactly the configured roles. A
// typo in the literal would silently empty every triage population instead of
// failing at start-up.
func TestTriageFolderRolesIsTheSchemaSetting(t *testing.T) {
	cfg := configtest.New(t, nil)
	if _, ok := cfg.Schema().Lookup(SettingTriageFolderRoles); !ok {
		t.Fatalf("%q is not a schema setting", SettingTriageFolderRoles)
	}
	roles := TriageFolderRoles(cfg)
	want := cfg.Strings(SettingTriageFolderRoles)
	if len(roles) == 0 || len(roles) != len(want) {
		t.Fatalf("roles %v, configured %v", roles, want)
	}
	for i, role := range roles {
		if string(role) != want[i] {
			t.Fatalf("role %d = %q, want %q", i, role, want[i])
		}
	}
	if !slices.Contains(roles, mail.RoleInbox) || !slices.Contains(roles, mail.RoleSent) {
		t.Fatalf("the default population lost its core roles: %v", roles)
	}
}
