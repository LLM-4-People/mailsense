package evidence

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config"
)

// settingKeys is every key this package names, with the schema type its reader
// expects. TestEverySettingConstantIsListed reads settings.go and requires this
// list to cover every setting constant, so a new one cannot skip the schema
// check.
var settingKeys = []struct {
	key string
	typ config.Type
}{
	{settingMinLetters, config.TypeInt},
	{settingLangChars, config.TypeInt},
	{settingMaxEdits, config.TypeInt},
	{settingMinNameChars, config.TypeInt},
	{settingTemplateCosine, config.TypeFloat},
	{settingTemplateWindow, config.TypeInt},
	{settingMaxDates, config.TypeInt},
	{settingBuckets, config.TypeList},
	{settingWorkers, config.TypeInt},
	{settingBatch, config.TypeInt},
	{settingAfterRuns, config.TypeBool},
	{settingRunsKeep, config.TypeInt},
}

// TestSettingConstantsResolve: every key the package reads exists in the
// schema, with the type its reader uses. A typo would silently read the schema
// default, which no behaviour test can see because the default is valid.
func TestSettingConstantsResolve(t *testing.T) {
	schema, err := config.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range settingKeys {
		setting, ok := schema.Lookup(tc.key)
		if !ok {
			t.Errorf("%q is not a schema setting", tc.key)
			continue
		}
		if setting.Type != tc.typ {
			t.Errorf("%q has schema type %q, the reader expects %q", tc.key, setting.Type, tc.typ)
		}
	}
}

// TestEverySettingConstantIsListed keeps the table above honest: a constant
// added to settings.go without a schema assertion fails here.
func TestEverySettingConstantIsListed(t *testing.T) {
	data, err := os.ReadFile("settings.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	declared := regexp.MustCompile(`(?m)^\t(?:Setting|setting)\w+\s*=`).FindAllString(source, -1)
	if len(declared) != len(settingKeys) {
		t.Fatalf("settings.go declares %d constants, the schema table checks %d", len(declared), len(settingKeys))
	}
	listed := make(map[string]bool, len(settingKeys))
	for _, tc := range settingKeys {
		listed[tc.key] = true
	}
	for _, match := range regexp.MustCompile(`(?m)^\tsetting\w+\s*=\s*"([^"]+)"`).FindAllStringSubmatch(source, -1) {
		if !listed[match[1]] {
			t.Errorf("%q is declared but missing from the schema table", match[1])
		}
	}
}

// TestNoSettingKeyLiteralAtReadSites: a read site never spells the key. Every
// accessor call with a string argument is rejected, whatever its prefix, so a
// future key cannot slip in as a literal.
func TestNoSettingKeyLiteralAtReadSites(t *testing.T) {
	readSite := regexp.MustCompile(`\.(Int|Bool|Duration|Float|String|Strings|Ints)\("([^"]*)"`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range readSite.FindAllStringSubmatch(string(data), -1) {
			t.Errorf("%s reads the key literal %s; use its setting constant", file, match[0])
		}
	}
}
