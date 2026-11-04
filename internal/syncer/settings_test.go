package syncer

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// settingKeys is every key this package names, with the schema type its reader
// expects. TestEverySettingConstantIsListed reads settings.go and requires this
// list to cover every Setting* constant, so a new one cannot skip the schema
// check.
var settingKeys = []struct {
	key string
	typ config.Type
}{
	{SettingBatchSize, config.TypeInt},
	{SettingMaxLoggedSkips, config.TypeInt},
	{SettingProgressInterval, config.TypeDuration},
	{SettingSkipRatioFatal, config.TypeFloat},
	{SettingDiscoveryInterval, config.TypeDuration},
	{SettingDetailsEnabled, config.TypeBool},
	{SettingDetailsBatch, config.TypeInt},
	{SettingDetailsMaxPerRun, config.TypeInt},
	{SettingDetailsSettleAfter, config.TypeDuration},
	{SettingRemovalHoldCount, config.TypeInt},
	{SettingRemovalHoldRatio, config.TypeFloat},
	{SettingRemovalAutoApplyAfter, config.TypeDuration},
	{SettingAuto, config.TypeBool},
	{SettingAutoInterval, config.TypeDuration},
	{SettingContactsSyncEnabled, config.TypeBool},
	{SettingCalendarSyncEnabled, config.TypeBool},
	{SettingCalendarPastDays, config.TypeInt},
	{SettingCalendarFutureDays, config.TypeInt},
	{SettingSubaddressSeparators, config.TypeList},
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
	if SettingBatchSize != store.SettingSyncBatchSize {
		t.Fatalf("the syncer and the store disagree on the write-batch key: %q vs %q", SettingBatchSize, store.SettingSyncBatchSize)
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
// run, and only a schema check on the constant would not see it.
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
