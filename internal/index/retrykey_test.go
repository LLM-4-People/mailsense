package index

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// TestRetryKeySharesTheCoverageRoleFingerprint: a backoff belongs to the exact
// index work that stopped, and the covered folder roles are one of its inputs.
// The retry key used to re-spell the join of index.folder_roles, so a change of
// the fingerprint format could silently differ from the coverage key and
// re-arm a wait for a scope that no longer exists (or keep one that does not).
func TestRetryKeySharesTheCoverageRoleFingerprint(t *testing.T) {
	// Deliberately not alphabetical: a re-spelled join keeps the config order
	// and would disagree with the canonical fingerprint.
	w := newWorld(t, map[string]any{SettingFolderRoles: []any{"sent", "inbox"}})
	key := w.ix.retryKeyFor("space", "v1")
	covered, _ := scopeOf(w.cfg.Snapshot())
	if want := rolesKey(covered); key.roles != want {
		t.Fatalf("retry roles %q, coverage roles %q", key.roles, want)
	}

	// Changing the covered roles changes the key, so a wait armed for one
	// scope cannot survive into another.
	if err := w.cfg.Set(SettingFolderRoles, []string{"inbox"}); err != nil {
		t.Fatal(err)
	}
	changed := w.ix.retryKeyFor("space", "v1")
	if changed.roles == key.roles {
		t.Fatalf("the roles change did not reach the retry key: %q", changed.roles)
	}
	if changed == key {
		t.Fatal("the retry key survived a change of the covered roles")
	}
}

// TestRolesKeyIsCanonical: the fingerprint is a set, not a list: reordering
// index.folder_roles is the same coverage (no needless cache miss), while
// different role sets never collide.
func TestRolesKeyIsCanonical(t *testing.T) {
	same := [][2][]mail.FolderRole{
		{{mail.RoleInbox, mail.RoleSent}, {mail.RoleSent, mail.RoleInbox}},
		{{mail.RoleInbox}, {mail.RoleInbox, mail.RoleInbox}}, // a repeated role is the same set
	}
	for _, pair := range same {
		if rolesKey(pair[0]) != rolesKey(pair[1]) {
			t.Errorf("%v and %v fingerprint differently", pair[0], pair[1])
		}
	}
	distinct := [][2][]mail.FolderRole{
		{{mail.RoleInbox, mail.RoleSent}, {mail.FolderRole("inboxsent")}},
		{{mail.RoleInbox}, {mail.RoleInbox, mail.RoleSent}},
		{{}, {mail.RoleInbox}},
		{{mail.RoleInbox, mail.RoleSent}, {mail.RoleInbox, mail.RoleArchive}},
	}
	for _, pair := range distinct {
		if rolesKey(pair[0]) == rolesKey(pair[1]) {
			t.Errorf("%v and %v collide", pair[0], pair[1])
		}
	}
}
