package ui

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/layer1"
	"github.com/LLM-4-People/mailsense/internal/prose"
)

// catalogEntry is one parsed value of a message catalog. The catalog format
// is a flat object: a key maps either to a string or to a plural object.
type catalogEntry struct {
	text     string
	plural   map[string]string
	isPlural bool
}

func readCatalog(t *testing.T, name string) map[string]catalogEntry {
	t.Helper()
	body, err := fs.ReadFile(Files, name)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	text := string(body)
	start := strings.Index(text, "= {")
	end := strings.LastIndex(text, "};")
	if start < 0 || end <= start {
		t.Fatalf("%s: no catalog object", name)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text[start+2:end+1]), &raw); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	out := make(map[string]catalogEntry, len(raw))
	for key, value := range raw {
		var v any
		if err := json.Unmarshal(value, &v); err != nil {
			t.Fatalf("%s: %s: %v", name, key, err)
		}
		switch typed := v.(type) {
		case string:
			out[key] = catalogEntry{text: typed}
		case map[string]any:
			forms := make(map[string]string, len(typed))
			for form, formValue := range typed {
				s, ok := formValue.(string)
				if !ok {
					t.Fatalf("%s: %s.%s is not a string", name, key, form)
				}
				forms[form] = s
			}
			out[key] = catalogEntry{plural: forms, isPlural: true}
		default:
			t.Fatalf("%s: %s has unsupported value %T", name, key, v)
		}
	}
	return out
}

var placeholderRE = regexp.MustCompile(`\{([A-Za-z0-9_]+)\}`)

func placeholders(text string) []string {
	seen := map[string]bool{}
	for _, match := range placeholderRE.FindAllStringSubmatch(text, -1) {
		seen[match[1]] = true
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func entryStrings(entry catalogEntry) []string {
	if !entry.isPlural {
		return []string{entry.text}
	}
	out := make([]string, 0, len(entry.plural))
	for form, text := range entry.plural {
		out = append(out, form+"="+text)
	}
	sort.Strings(out)
	return out
}

func checkPlaceholders(t *testing.T, name, key string, entry catalogEntry) {
	t.Helper()
	if entry.isPlural {
		for _, form := range []string{"one", "other"} {
			if strings.TrimSpace(entry.plural[form]) == "" {
				t.Errorf("%s: %s.%s is empty or missing", name, key, form)
			}
		}
		for form, text := range entry.plural {
			if strings.TrimSpace(text) == "" {
				t.Errorf("%s: %s.%s is empty", name, key, form)
			}
		}
		return
	}
	if strings.TrimSpace(entry.text) == "" {
		t.Errorf("%s: %s is empty", name, key)
	}
}

// frenchOnlyNamespace reports the namespaces whose wording belongs only to
// the French catalog: API codes fall back to the server's English message,
// and settings groups and descriptions fall back to the schema.
func frenchOnlyNamespace(key string) bool {
	switch {
	case strings.HasPrefix(key, "api."):
		return true
	case strings.HasPrefix(key, "settings.group."):
		return true
	case strings.HasPrefix(key, "settings.") && strings.HasSuffix(key, ".description"):
		return true
	}
	return false
}

func TestCatalogParity(t *testing.T) {
	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	if len(en) == 0 || len(fr) == 0 {
		t.Fatal("empty catalog")
	}
	for key, english := range en {
		checkPlaceholders(t, "i18n.en.js", key, english)
		french, ok := fr[key]
		if !ok {
			t.Errorf("i18n.fr.js is missing shared key %q", key)
			continue
		}
		if english.isPlural != french.isPlural {
			t.Errorf("%s changes shape between catalogs", key)
			continue
		}
		if english.isPlural {
			want := placeholders(english.plural["one"] + english.plural["other"])
			got := placeholders(french.plural["one"] + french.plural["other"])
			if strings.Join(want, ",") != strings.Join(got, ",") {
				t.Errorf("%s plural placeholders differ: en %v, fr %v", key, want, got)
			}
			for form := range english.plural {
				if _, ok := french.plural[form]; !ok {
					t.Errorf("%s: French lacks the %q form", key, form)
				}
			}
			continue
		}
		want, got := placeholders(english.text), placeholders(french.text)
		if strings.Join(want, ",") != strings.Join(got, ",") {
			t.Errorf("%s placeholders differ: en %v, fr %v", key, want, got)
		}
	}
	for key, french := range fr {
		checkPlaceholders(t, "i18n.fr.js", key, french)
		if _, ok := en[key]; ok {
			continue
		}
		if !frenchOnlyNamespace(key) {
			t.Errorf("i18n.fr.js has key %q outside the French-only namespaces", key)
		}
	}
}

// The schema owns setting metadata; the French catalog owns its wording.
// Every setting and group must have a French entry, and no stale entry may
// outlive the schema.
func TestFrenchSettingsCoverage(t *testing.T) {
	schema, err := config.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	fr := readCatalog(t, "i18n.fr.js")
	groups := map[string]bool{}
	for _, setting := range schema.Settings() {
		groups[setting.Group] = true
		if _, ok := fr["settings."+setting.Key+".description"]; !ok {
			t.Errorf("i18n.fr.js is missing settings.%s.description", setting.Key)
		}
	}
	for group := range groups {
		if _, ok := fr["settings.group."+group]; !ok {
			t.Errorf("i18n.fr.js is missing settings.group.%s", group)
		}
	}
	for key := range fr {
		switch {
		case strings.HasPrefix(key, "settings.group."):
			if !groups[strings.TrimPrefix(key, "settings.group.")] {
				t.Errorf("i18n.fr.js has a stale group translation %q", key)
			}
		case strings.HasPrefix(key, "settings.") && strings.HasSuffix(key, ".description"):
			setting := strings.TrimSuffix(strings.TrimPrefix(key, "settings."), ".description")
			if _, ok := schema.Lookup(setting); !ok {
				t.Errorf("i18n.fr.js has a stale setting translation %q", key)
			}
		}
	}
}

var uiTextLiteralRE = regexp.MustCompile(`(?:\b(?:text|"aria-label"|"aria-labelledby"|placeholder|title)\s*:\s*"([^"]*)"|\b(?:button|pill|badge|section|emptyState|loadingState|toast|tabButton)\(\s*"([^"]*)")`)

// The simple settings view, the learning summary and answer history all add
// shared wording. Plural shapes must stay identical so the UI never drops a
// count or renders an empty sentence.
func TestFriendlyScreenCatalogs(t *testing.T) {
	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	keys := []string{
		"settings.simple.title", "settings.simple.sub", "settings.simple.show_all",
		"settings.simple.back", "settings.simple.hidden", "settings.simple.count",
		"settings.meta.simple", "settings.meta.advanced", "settings.meta.shown",
		"settings.search.all", "settings.simple.group.language",
		"settings.simple.group.answers", "settings.simple.group.mail",
		"settings.simple.group.learning",
		"learning.action.answer", "learning.action.review", "learning.questions.title",
		"learning.questions.sub", "learning.technical_stage", "learning.technical_subject",
		"answers.header.title", "answers.header.sub",
		"labels.question.scroll_prev", "labels.question.scroll_next",
		"common.technical_details", "common.code",
	}
	for _, key := range keys {
		if _, ok := en[key]; !ok {
			t.Errorf("i18n.en.js lacks %q", key)
		}
		if _, ok := fr[key]; !ok {
			t.Errorf("i18n.fr.js lacks %q", key)
		}
	}
	for _, key := range []string{"settings.simple.hidden", "settings.simple.count", "settings.meta.shown", "learning.action.review"} {
		if !en[key].isPlural || !fr[key].isPlural {
			t.Errorf("%q must be a plural entry", key)
		}
		for _, form := range []string{"one", "other"} {
			if !strings.Contains(en[key].plural[form], "{count}") {
				t.Errorf("%s.%s lacks the count placeholder", key, form)
			}
			if !strings.Contains(fr[key].plural[form], "{count}") {
				t.Errorf("%s.%s (fr) lacks the count placeholder", key, form)
			}
		}
	}
	if !strings.Contains(strings.ToLower(en["settings.simple.hidden"].plural["other"]), "hidden") {
		t.Errorf("the English hidden-settings note is unclear: %q", en["settings.simple.hidden"].plural["other"])
	}
	if !strings.Contains(strings.ToLower(fr["settings.simple.hidden"].plural["other"]), "masqu") {
		t.Errorf("the French hidden-settings note is not natural: %q", fr["settings.simple.hidden"].plural["other"])
	}
}

// The visible count sentences must be plural entries: a French count of
// one rendered "1 réponses", "1 sélectionnées" or "1 décidés".
func TestVisibleCountStringsArePlural(t *testing.T) {
	keys := []struct{ key, frOne string }{
		{"sources.picked_count", "sélectionnée"},
		{"triage.decided", "décidé"},
		{"triage.row.answers", "répondue"},
		{"triage.shown", "affiché"},
		{"triage.filter_hidden", "ligne masquée"},
		{"sources.held.meta", "message stocké"},
		{"settings.providers.error.role_partial", "rôle enregistré"},
		{"companies.brief.span", "message depuis"},
		{"companies.brief.activity", "message sur"},
		{"common.result_count_at_least", "résultat"},
		{"triage.row.abstained_suffix", "abstention"},
		{"triage.row.missing_suffix", "manquante"},
		{"detail.missing", "manquante"},
		{"logs.file_failing", "erreur"},
		{"logs.meta.retained", "conservée"},
		{"logs.meta.paused", "nouvelle"},
		{"action_dialog.judge.summary", "message"},
		{"ask.table.truncated", "ligne"},
		{"agenda.backlog", "autre en attente"},
	}
	en, fr := readCatalog(t, "i18n.en.js"), readCatalog(t, "i18n.fr.js")
	for _, tc := range keys {
		if !en[tc.key].isPlural || !fr[tc.key].isPlural {
			t.Errorf("%q must be a plural entry", tc.key)
			continue
		}
		if !strings.Contains(fr[tc.key].plural["one"], tc.frOne) {
			t.Errorf("%s.one (fr) = %q, want the singular %q", tc.key, fr[tc.key].plural["one"], tc.frOne)
		}
	}
}

// TestNoHardcodedUIText keeps user-facing wording in the catalogs. Data
// values and brand/proper nouns are allow-listed; everything else must come
// from t()/tOr() or the caller-supplied data.
func TestNoHardcodedUIText(t *testing.T) {
	body, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{
		"":                true, // an explicit empty value where the control owner supplies the text
		"#":               true, // row-number prefix
		"v":               true, // version prefix
		"you@example.com": true, // example address, not prose
		"Europe/Paris":    true, // example IANA zone, not prose
	}
	for _, match := range uiTextLiteralRE.FindAllStringSubmatchIndex(string(body), -1) {
		value := ""
		if match[2] >= 0 {
			value = string(body[match[2]:match[3]])
		} else {
			value = string(body[match[4]:match[5]])
		}
		if allowed[value] {
			continue
		}
		line := 1 + strings.Count(string(body[:match[0]]), "\n")
		t.Errorf("app.js:%d passes the literal %q to a UI sink; use a catalog key", line, value)
	}
}

// TestCatalogCopyHasNoDashes keeps the owner-facing prose free of em dashes
// and double hyphens. It is a style guard, not a grammar check.
func TestCatalogCopyHasNoDashes(t *testing.T) {
	for _, file := range []string{"i18n.en.js", "i18n.fr.js"} {
		for key, entry := range readCatalog(t, file) {
			values := []string{entry.text}
			for _, form := range entry.plural {
				values = append(values, form)
			}
			for _, value := range values {
				for _, rule := range prose.Check(value) {
					t.Errorf("%s %q uses %s: %q", file, key, rule, value)
				}
			}
		}
	}
}

// TestAskSavedCopyIsClear protects the saved-answer destination: it must say
// it is an earlier answer and that the next message starts a new
// conversation, not a vague "saved answer".
func TestAskSavedCopyIsClear(t *testing.T) {
	en, fr := readCatalog(t, "i18n.en.js"), readCatalog(t, "i18n.fr.js")
	for _, tc := range []struct {
		name      string
		catalog   map[string]catalogEntry
		key, want string
		forbidden string
	}{
		{"en composer", en, "ask.composer.saved", "Earlier answer", "Saved answer"},
		{"en destination", en, "ask.composer.saved", "starts a new conversation", ""},
		{"fr composer", fr, "ask.composer.saved", "Ancienne réponse", "Réponse enregistrée"},
		{"fr destination", fr, "ask.composer.saved", "démarre une nouvelle conversation", ""},
		{"en thread", en, "ask.thread.saved", "Earlier answer", "Saved answer"},
		{"fr thread", fr, "ask.thread.saved", "Ancienne réponse", "Réponse enregistrée"},
	} {
		entry, ok := tc.catalog[tc.key]
		if !ok {
			t.Errorf("%s: %q is missing", tc.name, tc.key)
			continue
		}
		if !strings.Contains(entry.text, tc.want) || (tc.forbidden != "" && strings.Contains(entry.text, tc.forbidden)) {
			t.Errorf("%s: %q = %q, want %q without %q", tc.name, tc.key, entry.text, tc.want, tc.forbidden)
		}
	}
}

// TestAppCatalogKeys proves every literal t()/tOr() key in the shell exists
// in both catalogs. API fallbacks are French-only and guarded by I18N.has.
func TestAppCatalogKeys(t *testing.T) {
	body, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	re := regexp.MustCompile(`\bt(?:Or)?\(\s*"([^"]+)"`)
	for _, match := range re.FindAllStringSubmatch(string(body), -1) {
		key := match[1]
		if strings.HasSuffix(key, ".") || strings.HasPrefix(key, "api.") {
			continue
		}
		if _, ok := en[key]; !ok {
			t.Errorf("app.js uses %q, which i18n.en.js lacks", key)
		}
		if _, ok := fr[key]; !ok {
			t.Errorf("app.js uses %q, which i18n.fr.js lacks", key)
		}
	}
}

func TestShellI18nWiring(t *testing.T) {
	body, err := fs.ReadFile(Files, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(body)
	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	for _, key := range regexp.MustCompile(`data-i18n(?:-aria|-title|-placeholder)?="([^"]+)"`).FindAllStringSubmatch(page, -1) {
		if _, ok := en[key[1]]; !ok {
			t.Errorf("index.html references %q, which i18n.en.js lacks", key[1])
		}
		if _, ok := fr[key[1]]; !ok {
			t.Errorf("index.html references %q, which i18n.fr.js lacks", key[1])
		}
	}
	order := []string{`{{asset:i18n.en.js}}`, `{{asset:i18n.js}}`, `{{asset:app.js}}`}
	previous := -1
	for _, marker := range order {
		at := strings.Index(page, marker)
		if at < 0 {
			t.Fatalf("index.html lacks %s", marker)
		}
		if at <= previous {
			t.Fatalf("%s is out of order: the English catalog and the runtime must load before app.js", marker)
		}
		previous = at
	}
	// Every catalog but English is fetched only when the owner's language
	// needs it; the runtime owns that loader (one request per language).
	if strings.Contains(page, `{{asset:i18n.fr.js}}`) {
		t.Error("index.html loads the French catalog for every visitor")
	}
	runtime, err := fs.ReadFile(Files, "i18n.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(runtime), `assetURL("i18n." + base + ".js")`) || !strings.Contains(string(runtime), "function ensure(language)") {
		t.Error("i18n.js lacks the on-demand catalog loader")
	}
}

// TestI18nRuntimeBehavior runs the shell's real runtime under Node. Node is
// not a build dependency of the desktop app, so environments without it skip
// the behavioral half; the catalog parity test still checks every catalog.
func TestI18nRuntimeBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed: catalog parity and shell checks still run")
	}
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for _, name := range []string{"i18n.en.js", "i18n.fr.js", "i18n.js", "app.js"} {
		out, err := exec.CommandContext(ctx, node, "--check", filepath.Join(dir, name)).CombinedOutput()
		if err != nil {
			t.Fatalf("%s does not parse: %v\n%s", name, err, out)
		}
	}
	cmd := exec.CommandContext(ctx, node, filepath.Join(dir, "testdata", "i18n_behavior_test.js"), dir)
	cmd.Env = append(os.Environ(), "TZ=UTC")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("i18n runtime behavior timed out: %v\n%s", ctx.Err(), out)
	}
	if err != nil {
		t.Fatalf("i18n runtime behavior failed: %v\n%s", err, out)
	}
}

// TestJobStageKeys: every stage a job can report has a label in both
// catalogs. The stage keys have one owner (the layer1 constants); a new
// stage that forgets its translation fails here.
func TestJobStageKeys(t *testing.T) {
	stages := []string{layer1.StageDesign, layer1.StageGather, layer1.StageFit, layer1.StageFold,
		layer1.StageMeasure, layer1.StageCertify, layer1.StageSettle, layer1.StageAnswer}
	for _, name := range []string{"i18n.en.js", "i18n.fr.js"} {
		catalog := readCatalog(t, name)
		for _, stage := range stages {
			key := "jobs.stage." + stage
			if _, ok := catalog[key]; !ok {
				t.Errorf("%s is missing %q", name, key)
			}
		}
	}
}

var reasonLiteralRE = regexp.MustCompile(`(?:reasons = append\(reasons, |Reason: )"([a-z0-9_]+)"`)

// Every reason code a producer can emit has a translated badge in both
// catalogs. The codes are read from their owners so a new thread state or
// citation reason cannot ship as a raw snake_case token.
func TestReasonCodesHaveLabels(t *testing.T) {
	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	files := []string{
		filepath.Join("..", "priority", "priority.go"),
		filepath.Join("..", "qa", "person.go"),
		filepath.Join("..", "qa", "summary.go"),
	}
	codes := map[string]bool{}
	for _, name := range files {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, match := range reasonLiteralRE.FindAllStringSubmatch(string(body), -1) {
			codes[match[1]] = true
		}
	}
	if len(codes) < 6 {
		t.Fatalf("found only %d reason codes: %v", len(codes), codes)
	}
	for code := range codes {
		key := "reason." + code
		if _, ok := en[key]; !ok {
			t.Errorf("i18n.en.js lacks %s (emitted by a producer)", key)
		}
		if _, ok := fr[key]; !ok {
			t.Errorf("i18n.fr.js lacks %s (emitted by a producer)", key)
		}
	}
}

var apiCodeLiteralRE = regexp.MustCompile(`Code:\s*"([a-z0-9_]+)"`)

// Every API code the Go handlers return has a French sentence; the fallback
// would otherwise hide the actionable cause behind a generic error. A code
// built by concatenation (the "invalid_" prefix family) is not a literal and
// is covered by its concrete suffixes above.
func TestFrenchAPICodesCoverHandlers(t *testing.T) {
	fr := readCatalog(t, "i18n.fr.js")
	paths, err := filepath.Glob(filepath.Join("..", "api", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	codes := map[string]bool{}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for _, match := range apiCodeLiteralRE.FindAllStringSubmatch(string(body), -1) {
			code := match[1]
			if strings.HasSuffix(code, "_") {
				continue
			}
			codes[code] = true
		}
	}
	if len(codes) < 30 {
		t.Fatalf("found only %d API codes", len(codes))
	}
	for code := range codes {
		if _, ok := fr["api."+code]; !ok {
			t.Errorf("i18n.fr.js lacks api.%s, a code a handler returns", code)
		}
	}
}

// The agenda truncation notice and the capped search lower bound are
// owner-facing sentences in both catalogs. They are not count sentences
// themselves, so parity is the contract that keeps them in step.
func TestAgendaAndCappedSearchCopy(t *testing.T) {
	en, fr := readCatalog(t, "i18n.en.js"), readCatalog(t, "i18n.fr.js")
	for _, key := range []string{"agenda.truncated", "search.capped_total"} {
		if _, ok := en[key]; !ok {
			t.Errorf("i18n.en.js lacks %q", key)
		}
		if _, ok := fr[key]; !ok {
			t.Errorf("i18n.fr.js lacks %q", key)
		}
	}
	if !en["common.result_count_at_least"].isPlural || !fr["common.result_count_at_least"].isPlural {
		t.Error("common.result_count_at_least must be a plural entry")
	}
	for _, form := range []string{"one", "other"} {
		if !strings.Contains(en["common.result_count_at_least"].plural[form], "{count}") ||
			!strings.Contains(fr["common.result_count_at_least"].plural[form], "{count}") {
			t.Errorf("common.result_count_at_least.%s lacks the count placeholder", form)
		}
	}
}
