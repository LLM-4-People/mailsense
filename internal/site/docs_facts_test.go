package site

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config"
)

func readDoc(t *testing.T, root, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// oneLine folds a wrapped paragraph so a fact is matched across line breaks
// without depending on the wrap width.
func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func schemaInt(t *testing.T, key string) int {
	t.Helper()
	schema, err := config.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	setting, ok := schema.Lookup(key)
	if !ok {
		t.Fatalf("schema has no %s", key)
	}
	var value int
	if err := json.Unmarshal(setting.Default, &value); err != nil {
		t.Fatalf("%s default: %v", key, err)
	}
	return value
}

func schemaString(t *testing.T, key string) string {
	t.Helper()
	schema, err := config.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	setting, ok := schema.Lookup(key)
	if !ok {
		t.Fatalf("schema has no %s", key)
	}
	var value string
	if err := json.Unmarshal(setting.Default, &value); err != nil {
		t.Fatalf("%s default: %v", key, err)
	}
	return value
}

// TestPublicPagesStateTheSchemaDefaults keeps the default values copied into
// the bilingual guide pages tied to the schema keys that own them. The pages
// are authored, not generated, so this is a contract test rather than a second
// renderer.
func TestPublicPagesStateTheSchemaDefaults(t *testing.T) {
	root := root(t)
	cpu := schemaInt(t, "resource.cpu.max_percent")
	ram := schemaInt(t, "resource.ram.max_percent")
	disk := schemaInt(t, "resource.disk.max_percent")
	for _, check := range []struct {
		file string
		want string
	}{
		{"docs/first-run.md", fmt.Sprintf("%d%% CPU", cpu)},
		{"docs/first-run.md", fmt.Sprintf("%d%% memory", ram)},
		{"docs/first-run.md", fmt.Sprintf("%d%% disk", disk)},
		{"docs/fr/first-run.md", fmt.Sprintf("%d %% du CPU", cpu)},
		{"docs/fr/first-run.md", fmt.Sprintf("%d %% de la mémoire", ram)},
		{"docs/fr/first-run.md", fmt.Sprintf("%d %% du disque", disk)},
	} {
		if !strings.Contains(oneLine(readDoc(t, root, check.file)), check.want) {
			t.Errorf("%s does not state %q", check.file, check.want)
		}
	}

	hold := schemaInt(t, "sync.removal.hold_count")
	delay := strings.TrimSuffix(schemaString(t, "sync.removal.auto_apply_after"), "h")
	if _, err := strconv.Atoi(delay); err != nil {
		t.Fatalf("sync.removal.auto_apply_after is not a whole number of hours: %v", err)
	}
	for _, check := range []struct {
		file string
		want string
	}{
		{"docs/mailboxes.md", fmt.Sprintf("at least %d messages", hold)},
		{"docs/fr/mailboxes.md", fmt.Sprintf("au moins %d messages", hold)},
		{"docs/mailboxes.md", fmt.Sprintf("after %s hours", delay)},
		{"docs/fr/mailboxes.md", fmt.Sprintf("après %s heures", delay)},
	} {
		if !strings.Contains(oneLine(readDoc(t, root, check.file)), check.want) {
			t.Errorf("%s does not state %q", check.file, check.want)
		}
	}
}

// TestLearningPagesStateTheRequirements ties the certification numbers in both
// learning pages to the embedded requirements document.
func TestLearningPagesStateTheRequirements(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(root(t), "internal", "layer1", "requirements.json"))
	if err != nil {
		t.Fatal(err)
	}
	var requirements struct {
		Defaults struct {
			MinEffectiveTrain int     `json:"min_effective_train"`
			MinEvalLabels     int     `json:"min_eval_labels"`
			TargetAccuracy    float64 `json:"target_accuracy"`
			MinCoverage       float64 `json:"min_coverage"`
		} `json:"defaults"`
	}
	if err := json.Unmarshal(body, &requirements); err != nil {
		t.Fatal(err)
	}
	want := requirements.Defaults
	en := regexp.MustCompile(`(?s)The default target is\s+([0-9.]+), the default minimum coverage is\s+([0-9.]+), and the default requirements ask for\s+at least (\d+) training labels and (\d+) evaluation labels\.`)
	fr := regexp.MustCompile(`(?s)La cible par défaut est\s+([0-9,]+), la couverture minimale par défaut est\s+([0-9,]+), et les exigences par défaut demandent au moins (\d+) étiquettes\s+d'entraînement et (\d+) étiquettes d'évaluation\.`)
	for _, check := range []struct {
		file    string
		pattern *regexp.Regexp
		comma   bool
	}{
		{"docs/learning.md", en, false},
		{"docs/fr/learning.md", fr, true},
	} {
		match := check.pattern.FindStringSubmatch(readDoc(t, root(t), check.file))
		if match == nil {
			t.Errorf("%s does not state the certification requirements", check.file)
			continue
		}
		numbers := make([]float64, 0, 4)
		for _, text := range match[1:] {
			if check.comma {
				text = strings.ReplaceAll(text, ",", ".")
			}
			value, err := strconv.ParseFloat(text, 64)
			if err != nil {
				t.Fatalf("%s: %v", check.file, err)
			}
			numbers = append(numbers, value)
		}
		if numbers[0] != want.TargetAccuracy || numbers[1] != want.MinCoverage ||
			numbers[2] != float64(want.MinEffectiveTrain) || numbers[3] != float64(want.MinEvalLabels) {
			t.Errorf("%s states %v, want target %v, coverage %v, train %d, eval %d",
				check.file, numbers, want.TargetAccuracy, want.MinCoverage, want.MinEffectiveTrain, want.MinEvalLabels)
		}
	}
}
