package store

import (
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/scope"
)

// The SQL fragments that filter or order by a vocabulary spell it from the
// Go owner; this pins the rendered literal to that constant so a re-spelled
// copy cannot drift back in at the query site.
func TestSQLVocabularyMatchesDomainConstants(t *testing.T) {
	if want := "v.verdict = '" + string(scope.Owner) + "'"; ownerWhere != want {
		t.Fatalf("ownerWhere = %q, want %q", ownerWhere, want)
	}
	if want := "f.role NOT IN ('" + string(mail.RoleDrafts) + "', '" + string(mail.RoleDeleted) + "', '" +
		string(mail.RoleJunk) + "')"; threadContentRoleWhere != want {
		t.Fatalf("threadContentRoleWhere = %q, want %q", threadContentRoleWhere, want)
	}
	// The precedence ranks the owner's manual label above the judge's
	// reference label above the judge's production answer, spelling each
	// source from its Go owner.
	manual := strings.Index(labelPrecedenceSQL, "'"+LabelManual+"'")
	judge := strings.Index(labelPrecedenceSQL, "'"+LabelJudge+"'")
	answer := strings.Index(labelPrecedenceSQL, "'"+LabelJudgeAnswer+"'")
	if manual < 0 || judge < 0 || answer < 0 || !(manual < judge && judge < answer) {
		t.Fatalf("labelPrecedenceSQL = %q, want manual < judge < judge_answer", labelPrecedenceSQL)
	}
	// The learned-fact read keeps manual and judge only: the production
	// answer is audit data, never a prior.
	if !strings.Contains(labelContextSQL, "'"+LabelManual+"'") || !strings.Contains(labelContextSQL, "'"+LabelJudge+"'") {
		t.Fatalf("labelContextSQL = %q, want the reference sources", labelContextSQL)
	}
	if strings.Contains(labelContextSQL, "'"+LabelJudgeAnswer+"'") {
		t.Fatalf("labelContextSQL = %q, must omit the production answer", labelContextSQL)
	}
}
