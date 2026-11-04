package mail

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
)

// settingKeys is every key this package names, with the schema type its reader
// expects. TestEverySettingConstantIsListed reads settings.go and requires this
// list to cover every Setting* constant, so a new one cannot skip the schema
// check.
var settingKeys = []struct {
	key string
	typ config.Type
}{
	{SettingSubjectPrefixes, config.TypeList},
	{SettingSubjectPrefixesLearned, config.TypeList},
	{SettingSharedDomains, config.TypeList},
	{SettingNoDateYearAtMost, config.TypeInt},
	{SettingNoDateYearAtLeast, config.TypeInt},
	{SettingBodyMaxChars, config.TypeInt},
	{SettingHTMLMaxChars, config.TypeInt},
	{SettingHeadersMaxChars, config.TypeInt},
	{SettingInlinePartMaxBytes, config.TypeInt},
	{SettingInlineTotalMaxBytes, config.TypeInt},
	{SettingInlineMaxParts, config.TypeInt},
}

// TestSettingConstantsResolve: every key the package reads exists in the
// schema, with the type its reader uses. A typo in a constant would otherwise
// read the schema default silently (or panic on the wrong accessor), which no
// behaviour test can see because the default is a valid value.
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
	declared := regexp.MustCompile(`(?m)^\tSetting\w+\s*=`).FindAllString(source, -1)
	if len(declared) != len(settingKeys) {
		t.Fatalf("settings.go declares %d constants, the schema table checks %d", len(declared), len(settingKeys))
	}
	listed := make(map[string]bool, len(settingKeys))
	for _, tc := range settingKeys {
		listed[tc.key] = true
	}
	for _, match := range regexp.MustCompile(`(?m)^\tSetting\w+\s*=\s*"([^"]+)"`).FindAllStringSubmatch(source, -1) {
		if !listed[match[1]] {
			t.Errorf("%q is declared but missing from the schema table", match[1])
		}
	}
}

// TestNoSettingKeyLiteralAtReadSites: the whole point of the constants is that
// a read site never spells the key. A re-introduced literal would compile and
// run, and only a schema check on the constant would not see it. Every
// accessor call with a string argument is rejected, whatever its prefix, so a
// future key cannot slip in as a literal just because it starts elsewhere.
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
			t.Errorf("%s reads the key literal %s; use its Setting* constant", file, match[0])
		}
	}
}

// TestLimitsFromConfigReadsEachSchemaKey pins the six Limits fields to their
// schema keys and their schema defaults: a swapped field or a constant pointing
// at another key would otherwise still return a plausible size.
func TestLimitsFromConfigReadsEachSchemaKey(t *testing.T) {
	fields := []struct {
		name string
		key  string
		got  func(Limits) int
	}{
		{"BodyChars", "mail.body.max_chars", func(l Limits) int { return l.BodyChars }},
		{"HTMLChars", "mail.body.html_max_chars", func(l Limits) int { return l.HTMLChars }},
		{"HeaderChars", "mail.headers.max_chars", func(l Limits) int { return l.HeaderChars }},
		{"InlinePartBytes", "mail.inline_assets.max_part_bytes", func(l Limits) int { return l.InlinePartBytes }},
		{"InlineTotalBytes", "mail.inline_assets.max_total_bytes", func(l Limits) int { return l.InlineTotalBytes }},
		{"InlineParts", "mail.inline_assets.max_parts", func(l Limits) int { return l.InlineParts }},
	}
	defaults, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	lim := LimitsFromConfig(defaults)
	for _, tc := range fields {
		want := defaults.Int(tc.key)
		if got := tc.got(lim); got != want {
			t.Errorf("default Limits.%s = %d, the schema's %s is %d", tc.name, got, tc.key, want)
		}
	}

	overrides := map[string]any{}
	for i, tc := range fields {
		overrides[tc.key] = 101 + i // distinct, and inside every cap's schema bounds
	}
	lim = LimitsFromConfig(configtest.New(t, overrides).Snapshot())
	for i, tc := range fields {
		if got, want := tc.got(lim), 101+i; got != want {
			t.Errorf("overridden Limits.%s = %d, want %d", tc.name, got, want)
		}
	}
}
