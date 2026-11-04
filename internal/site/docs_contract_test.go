package site

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config"
)

// settingCountProblems checks every settings count stated in docs/API.md
// against the one owner, the embedded configuration schema. The settings route
// sample is scoped to its row so other endpoint samples may use "total"
// freely, and an explicit "<n> settings" phrase is checked too.
func settingCountProblems(text string, want int) []string {
	var problems []string
	settingsRow := regexp.MustCompile("(?m)^.*`GET /api/settings`.*$")
	total := regexp.MustCompile(`"total":(\d+)`)
	found := 0
	for _, line := range settingsRow.FindAllString(text, -1) {
		for _, match := range total.FindAllStringSubmatch(line, -1) {
			found++
			value, err := strconv.Atoi(match[1])
			if err != nil || value != want {
				problems = append(problems, "settings row states total "+match[1]+", want "+strconv.Itoa(want))
			}
		}
	}
	if found == 0 {
		problems = append(problems, "settings row states no total")
	}
	phrase := regexp.MustCompile(`\b(\d+)\s+(?:settings|setting keys)\b`)
	for _, match := range phrase.FindAllStringSubmatch(text, -1) {
		value, err := strconv.Atoi(match[1])
		if err != nil || value != want {
			problems = append(problems, "prose states "+match[1]+" settings, want "+strconv.Itoa(want))
		}
	}
	return problems
}

// TestAPIDocumentStatesTheSchemaSettingCount is the contract test for the one
// hand-copied number in docs/API.md. The docs are authored, not generated, so
// the test enforces the count the schema owns instead of adding a second
// generation path.
func TestAPIDocumentStatesTheSchemaSettingCount(t *testing.T) {
	schema, err := config.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	want := len(schema.Settings())
	body, err := os.ReadFile(filepath.Join(root(t), "docs", "API.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, problem := range settingCountProblems(string(body), want) {
		t.Error(problem)
	}
}

// TestSettingCountCheckBites proves the contract test rejects a stale number
// instead of only accepting the current one.
func TestSettingCountCheckBites(t *testing.T) {
	stale := "| `GET /api/settings` | — | `200` `{\"total\":250}` | the full schema count |"
	problems := settingCountProblems(stale, 261)
	if len(problems) == 0 || !strings.Contains(strings.Join(problems, "; "), "250") {
		t.Fatalf("settingCountProblems = %v, want a stale count problem", problems)
	}
	if problems := settingCountProblems(stale, 250); len(problems) != 0 {
		t.Fatalf("settingCountProblems on a matching count = %v, want none", problems)
	}
}
