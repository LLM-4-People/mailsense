package mail

import (
	"slices"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config"
)

// Review L0 rules §3 (the role list was written out five times): the
// roles are code meaning here and data once in the schema's vocabulary
// folder_role, which every role setting references; both must agree.
func TestRolesAreTheSchemasVocabulary(t *testing.T) {
	s, err := config.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range Roles() {
		got = append(got, string(r))
	}
	if want := s.Vocabulary("folder_role"); !slices.Equal(got, want) {
		t.Fatalf("mail.Roles %v, schema folder_role %v", got, want)
	}
	for _, st := range s.Settings() {
		if strings.HasSuffix(st.Key, ".folder_roles") && !strings.HasSuffix(st.Format, ":folder_role") {
			t.Errorf("%s does not use the folder_role vocabulary (format %q)", st.Key, st.Format)
		}
	}
}
