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
	"docs/console.md",
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
	"docs/fr/console.md",
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
// uses an em dash, a spaced en dash, a double hyphen as punctuation, a stock
// phrase, a Title Case heading, a rhetorical transition or a closing paragraph
// that only repeats the opening. The rule itself lives in Check and
// CheckDocument, so the documentation test and the UI catalog test cannot
// drift.
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

// lineRuleCase is one positive case for one line rule. heading tells the rule
// set whether the input is a Markdown heading.
type lineRuleCase struct {
	name    string
	text    string
	heading bool
	rule    string
}

var lineRuleCases = []lineRuleCase{
	{"em dash", "A sentence \u2014 a clause.", false, RuleEmDash},
	{"spaced en dash", "A sentence \u2013 a clause.", false, RuleEnDash},
	{"spaced double hyphen", "A sentence -- a clause.", false, RuleDoubleDash},
	{"glued double hyphen", "A word--another.", false, RuleDoubleDash},
	{"delve", "We delve into the archive.", false, RuleStockPhrase},
	{"leverage as a verb", "We leverage the local index.", false, RuleStockPhrase},
	{"leverage with a pronoun", "I leverage remote models.", false, RuleStockPhrase},
	{"it is worth noting", "It is worth noting that the index is local.", false, RuleStockPhrase},
	{"importantly", "Importantly, the data stays local.", false, RuleStockPhrase},
	{"moreover", "Moreover, the store is local.", false, RuleStockPhrase},
	{"furthermore", "Furthermore, the store is local.", false, RuleStockPhrase},
	{"in conclusion", "In conclusion, the data stays local.", false, RuleStockPhrase},
	{"not only but also", "The design is not only local but also private.", false, RuleStockPhrase},
	{"it is not X it is Y", "It is not a bug, it is a feature.", false, RuleStockPhrase},
	{"French stock phrase", "Une solution v\u00e9ritable.", false, RuleStockPhrase},
	{"title case heading", "## Install Mailsense On Windows", true, RuleTitleCase},
	{"two word title case", "## Getting Started", true, RuleTitleCase},
	{"rhetorical question", "Ready to start?", false, RuleRhetoricalQuestion},
	{"ordered rhetorical question", "1. Ready to start?", false, RuleRhetoricalQuestion},
	{"French rhetorical question", "Et si vous commenciez ?", false, RuleRhetoricalQuestion},
}

// TestEveryLineRuleBites proves each declared line rule has an input that
// triggers it. A rule that no input triggers is dead code, and a guard that
// cannot fail is not a guard.
func TestEveryLineRuleBites(t *testing.T) {
	for _, tc := range lineRuleCases {
		t.Run(tc.name, func(t *testing.T) {
			if !contains(checkWithRules(tc.text, tc.heading, lineRules), tc.rule) {
				t.Errorf("checkWithRules(%q, heading=%v) = %v, want %q",
					tc.text, tc.heading, checkWithRules(tc.text, tc.heading, lineRules), tc.rule)
			}
		})
	}
}

// TestEveryLineRuleHasACase keeps the case table and the rule table the same
// size, so a new rule cannot land without a positive case.
func TestEveryLineRuleHasACase(t *testing.T) {
	covered := make(map[string]bool, len(lineRuleCases))
	for _, tc := range lineRuleCases {
		covered[tc.rule] = true
	}
	for _, rule := range lineRules {
		if !covered[rule.name] {
			t.Errorf("rule %q has no positive case", rule.name)
		}
	}
}

// TestRemovingALineRuleLosesItsCase is the mutation proof: drop one rule from
// the canonical set and its positive case stops failing.
func TestRemovingALineRuleLosesItsCase(t *testing.T) {
	for _, tc := range lineRuleCases {
		t.Run(tc.name, func(t *testing.T) {
			trimmed := rulesWithout(lineRules, tc.rule)
			if contains(checkWithRules(tc.text, tc.heading, trimmed), tc.rule) {
				t.Errorf("removing %q still reports it for %q", tc.rule, tc.text)
			}
		})
	}
}

// stockPhraseCases cover the markers the release plan names one by one. They
// are separate from lineRuleCases because several markers share one rule.
var stockPhraseCases = []struct {
	label string
	text  string
}{
	{"delve", "We delve into the archive."},
	{"leverage", "We leverage the local index."},
	{"it is worth noting", "It is worth noting that the index is local."},
	{"importantly", "Importantly, the data stays local."},
	{"moreover", "Moreover, the store is local."},
	{"furthermore", "Furthermore, the store is local."},
	{"in conclusion", "In conclusion, the data stays local."},
	{"not only ... but also", "The design is not only local but also private."},
	{"it is not X, it is Y", "It is not a bug, it is a feature."},
	{"non seulement ... mais aussi", "Non seulement local, mais aussi privé."},
	{"ce n'est pas X, c'est Y", "Ce n'est pas un bogue, c'est une fonction."},
	{"il est important de noter", "Il est important de noter que le stockage est local."},
}

// TestStockPhrasesBite proves each named marker is live and that removing it
// from a copy of the list removes the match.
func TestStockPhrasesBite(t *testing.T) {
	for _, tc := range stockPhraseCases {
		t.Run(tc.label, func(t *testing.T) {
			if !stockPhraseHit(tc.text, stockPhrases) {
				t.Fatalf("stockPhrases does not match %q", tc.text)
			}
			trimmed := stockPhrasesWithout(tc.label)
			if stockPhraseHit(tc.text, trimmed) {
				t.Errorf("removing %q still matches %q", tc.label, tc.text)
			}
		})
	}
}

// TestCleanCopyPasses is the other half of the mutation: the guard must not
// reject plain sentences, a command flag, an identifier, a range, a table
// separator, a URL, a proper noun in a sentence-case heading, or a noun use of
// "leverage".
func TestCleanCopyPasses(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		heading bool
	}{
		{"English sentence", "A short sentence about mail.", false},
		{"French sentence", "Une phrase simple sur le courrier.", false},
		{"flag", "Run sha256sum -c SHA256SUMS --ignore-missing.", false},
		{"two flags", "Use --data-dir and --headless.", false},
		{"table separator", "| --- | --- |", false},
		{"numeric range", "A range 1,3\u20131,8x.", false},
		{"spaced numeric range", "A range 1,3 \u2013 1,8x.", false},
		{"token range", "ring {from}\u2013{to}", false},
		{"identifier", "The build tag v1--stable is internal.", false},
		{"bare identifier", "foo--bar", false},
		{"url", "See https://example.test/a--b for the flag.", false},
		{"leverage noun", "The leverage of a small team is limited.", false},
		{"not only alone", "Not only one mailbox is picked.", false},
		{"it is not alone", "It is not stored.", false},
		{"sentence case with proper noun", "## Install Mailsense on Windows", true},
		{"unknown proper noun", "## Install Kubernetes on Windows", true},
		{"question with proper noun", "## Why Mailsense?", true},
		{"colon then lower case", "## Troubleshooting: mail does not sync", true},
		{"French sentence case", "## Guide d'installation", true},
		{"acronym heading", "## Source actions in the local API", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if rules := checkWithRules(tc.text, tc.heading, lineRules); len(rules) != 0 {
				t.Errorf("checkWithRules(%q, heading=%v) = %v, want none", tc.text, tc.heading, rules)
			}
		})
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

// TestCheckDocumentFlagsTitleCaseHeading keeps the heading rule wired into the
// document scan, not only into the unit helper.
func TestCheckDocumentFlagsTitleCaseHeading(t *testing.T) {
	body := "# Install Mailsense On Windows\n\nA short sentence.\n"
	findings := CheckDocument("sample.md", []byte(body))
	if len(findings) != 1 {
		t.Fatalf("got %d findings, want 1: %v", len(findings), findings)
	}
	if findings[0].Line != 1 || findings[0].Rule != RuleTitleCase {
		t.Errorf("finding = %+v, want line 1 title case heading", findings[0])
	}
}

// TestQuestionHeadingsStayAllowed proves a genuine FAQ heading is not treated
// as a rhetorical transition while the same sentence in the body is.
func TestQuestionHeadingsStayAllowed(t *testing.T) {
	allowed := "## Why is the inbox empty on Linux?\n\nThe answer names the source.\n"
	if findings := CheckDocument("sample.md", []byte(allowed)); len(findings) != 0 {
		t.Errorf("question heading produced %v", findings)
	}
	flagged := "A paragraph.\n\nReady to start?\n"
	findings := CheckDocument("sample.md", []byte(flagged))
	if len(findings) != 1 || findings[0].Rule != RuleRhetoricalQuestion || findings[0].Line != 3 {
		t.Errorf("findings = %v, want line 3 rhetorical question", findings)
	}
}

// closingCase is one positive or allowed closing paragraph.
type closingCase struct {
	name    string
	opening string
	closing string
	want    string
}

var closingCases = []closingCase{
	{
		name:    "restatement",
		opening: "Mailsense reads the local Outlook store and keeps every message on this machine.",
		closing: "Mailsense keeps every message on this machine and reads the local Outlook store.",
		want:    RuleClosingRestatement,
	},
	{
		name:    "marked restatement",
		opening: "Mailsense reads the local Outlook store and keeps every message on this machine.",
		closing: "In short, Mailsense reads the local Outlook store and keeps every message on this machine.",
		want:    RuleClosingRestatement,
	},
	{
		name:    "new fact",
		opening: "Mailsense reads the local Outlook store and keeps every message on this machine.",
		closing: "The installer also writes a desktop entry, and the next run reuses it from the same folder.",
		want:    "",
	},
}

// TestClosingRestatementBites covers the document rule with a restatement, a
// marked restatement and a closing that adds a new fact.
func TestClosingRestatementBites(t *testing.T) {
	for _, tc := range closingCases {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.opening + "\n\nA middle paragraph that names the installer.\n\n" + tc.closing + "\n"
			findings := CheckDocument("sample.md", []byte(body))
			got := ""
			for _, finding := range findings {
				if finding.Rule == RuleClosingRestatement {
					got = finding.Rule
				}
			}
			if got != tc.want {
				t.Errorf("closing rule = %q, want %q (findings %v)", got, tc.want, findings)
			}
		})
	}
}

// TestRemovingClosingRuleLosesItsCase is the document-rule mutation proof.
func TestRemovingClosingRuleLosesItsCase(t *testing.T) {
	tc := closingCases[0]
	body := tc.opening + "\n\nA middle paragraph that names the installer.\n\n" + tc.closing + "\n"
	findings := checkDocumentWith("sample.md", []byte(body), lineRules, nil)
	for _, finding := range findings {
		if finding.Rule == RuleClosingRestatement {
			t.Fatalf("removing the closing rule still reports %v", finding)
		}
	}
}

func rulesWithout(rules []lineRule, name string) []lineRule {
	trimmed := make([]lineRule, 0, len(rules))
	for _, rule := range rules {
		if rule.name != name {
			trimmed = append(trimmed, rule)
		}
	}
	return trimmed
}

func stockPhrasesWithout(label string) []stockPhrase {
	trimmed := make([]stockPhrase, 0, len(stockPhrases))
	for _, phrase := range stockPhrases {
		if phrase.label != label {
			trimmed = append(trimmed, phrase)
		}
	}
	return trimmed
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
