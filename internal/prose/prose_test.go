package prose

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// publicFiles is the closed list of prose that ships to a reader. Engineering
// logs, legal text, and generated references are deliberately absent: their
// dashes and symbols are facts, not copy.
var publicFiles = []string{
	"README.md",
	"README.fr.md",
	"CHANGELOG.md",
	"SECURITY.md",
	"CONTRIBUTING.md",
	"CODE_OF_CONDUCT.md",
	".github/pull_request_template.md",
	"docs/index.md",
	"docs/install.md",
	"docs/first-run.md",
	"docs/mailboxes.md",
	"docs/providers.md",
	"docs/learning.md",
	"docs/faq.md",
	"docs/privacy.md",
	"docs/security.md",
	"docs/contributing.md",
	"docs/troubleshooting.md",
	"docs/SELLING.md",
	"docs/fr/index.md",
	"docs/fr/install.md",
	"docs/fr/first-run.md",
	"docs/fr/mailboxes.md",
	"docs/fr/providers.md",
	"docs/fr/learning.md",
	"docs/fr/faq.md",
	"docs/fr/privacy.md",
	"docs/fr/security.md",
	"docs/fr/contributing.md",
	"docs/fr/troubleshooting.md",
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// TestPublicCopyFollowsTheRules is the release guard. It fails when public copy
// uses an em dash, an en dash, a double hyphen as punctuation, or a stock
// phrase. The rule itself lives in Check, so the UI catalog test and this test
// cannot drift.
func TestPublicCopyFollowsTheRules(t *testing.T) {
	root := repoRoot(t)
	for _, name := range publicFiles {
		body, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		for _, finding := range CheckDocument(name, body) {
			t.Error(finding.String())
		}
	}
}

// TestEveryRuleBites proves each declared rule has an input that triggers it.
// A rule that no input triggers is dead code, and a guard that cannot fail is
// not a guard.
func TestEveryRuleBites(t *testing.T) {
	cases := []struct {
		name string
		text string
		rule string
	}{
		{"em dash", "A sentence \u2014 a clause.", RuleEmDash},
		{"spaced en dash", "A sentence \u2013 a clause.", RuleEnDash},
		{"spaced double hyphen", "A sentence -- a clause.", RuleDoubleDash},
		{"glued double hyphen", "A word--another.", RuleDoubleDash},
		{"English stock phrase", "We delve into the work.", RuleStockPhrase},
		{"French stock phrase", "Une solution v\u00e9ritable.", RuleStockPhrase},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !contains(Check(tc.text), tc.rule) {
				t.Errorf("Check(%q) = %v, want %q", tc.text, Check(tc.text), tc.rule)
			}
		})
	}
}

// TestCleanCopyPasses is the other half of the mutation: the guard must not
// reject plain sentences, a command flag, or a table separator.
func TestCleanCopyPasses(t *testing.T) {
	for _, text := range []string{
		"A short sentence about mail.",
		"Une phrase simple sur le courrier.",
		"Run sha256sum -c SHA256SUMS --ignore-missing.",
		"| --- | --- |",
		"A range 1,3\u20131,8x.",
		"ring {from}\u2013{to}",
	} {
		if rules := Check(text); len(rules) != 0 {
			t.Errorf("Check(%q) = %v, want none", text, rules)
		}
	}
}

// TestCheckDocumentSkipsCode proves a fenced block and an inline span may use
// hyphens freely while the prose around them is still checked.
func TestCheckDocumentSkipsCode(t *testing.T) {
	body := strings.Join([]string{
		"# Title",
		"",
		"```bash",
		"mailsense --headless --data-dir /tmp/mailsense",
		"```",
		"",
		"Run `mailsense --help` first.",
		"",
		"This -- is prose.",
	}, "\n")
	findings := CheckDocument("sample.md", []byte(body))
	if len(findings) != 1 {
		t.Fatalf("got %d findings, want 1: %v", len(findings), findings)
	}
	if findings[0].Line != 9 || findings[0].Rule != RuleDoubleDash {
		t.Errorf("finding = %+v, want line 9 double hyphen", findings[0])
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
