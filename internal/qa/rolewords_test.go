package qa

import (
	"os"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/search"
)

// TestRoleMailboxVocabularyHasOneOwner: a shared mailbox is a role everywhere it
// is judged. The display-name rule, the local-part rule, the company-contact
// desk list and the person-name guard used two lists that had drifted, so "RH"
// was a role for entity linking and a person for a name question.
func TestRoleMailboxVocabularyHasOneOwner(t *testing.T) {
	data, err := os.ReadFile("rolewords.go")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "var roleMailboxWords ="); got != 1 {
		t.Fatalf("rolewords.go defines roleMailboxWords %d times, want once", got)
	}
	for _, name := range []string{"genericPersonTokens", "mailboxWords"} {
		for _, file := range []string{"rolewords.go", "entity.go", "person.go", "company_match.go"} {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), name) {
				t.Errorf("%s still names the retired vocabulary %q", file, name)
			}
		}
	}
	for _, file := range []string{"entity.go", "person.go", "company_match.go"} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "roleMailboxWords") {
			t.Errorf("%s does not read the shared role vocabulary", file)
		}
	}
}

// TestRoleMailboxVocabularyCoversBothLists: the union of the two retired lists,
// so a word that was a role in one path is one in every path.
func TestRoleMailboxVocabularyCoversBothLists(t *testing.T) {
	for _, word := range []string{
		"info", "support", "service", "services", "team", "admin", "billing", "noreply",
		"rh", "hr", "facturation", "comptabilite", "commandes", "direction", "secretariat",
		"accounts", "microsoft", "google", "notifications",
	} {
		if !roleMailboxWords[word] {
			t.Errorf("roleMailboxWords lacks %q", word)
		}
	}
	for _, word := range []string{"elodie", "martin", "acme", "atelier-nord"} {
		if roleMailboxWords[word] {
			t.Errorf("roleMailboxWords wrongly treats %q as a role", word)
		}
	}
}

// TestRoleDisplayNamesAreNotPeople: a two-word display name containing any role
// word is a service identity, not a person.
func TestRoleDisplayNamesAreNotPeople(t *testing.T) {
	for _, c := range []struct {
		label string
		want  bool
	}{
		{"RH Support", false},
		{"Microsoft 365", false},
		{"Comptabilité Clients", false},
		{"Élodie Martin", true},
		{"Simon Berthier", true},
	} {
		got := resolvedEntity{Kind: entityPerson, Label: c.label}.isPersonLike()
		if got != c.want {
			t.Errorf("isPersonLike(%q) = %v, want %v", c.label, got, c.want)
		}
	}
	if got := personName("rh support", []search.Hit{{SenderName: "RH Support"}}); got != "" {
		t.Errorf("personName picked the role mailbox %q", got)
	}
}
