package ui

import (
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"
)

// TestSetupScreenStaysWiredOutsideTheRouter protects the first-run overlay.
// Setup is deliberately not a route: adding it to SCREEN_IDS changes the
// router contract and every screen test. It is a boot-time overlay over the
// existing screens, and this test keeps it that way.
func TestSetupScreenStaysWiredOutsideTheRouter(t *testing.T) {
	jsNames, err := moduleNames("js", ".js")
	if err != nil {
		t.Fatal(err)
	}
	cssNames, err := moduleNames("css", ".css")
	if err != nil {
		t.Fatal(err)
	}
	if !containsName(jsNames, "05-setup.js") {
		t.Fatal("05-setup.js is not in the js manifest")
	}
	if !containsName(cssNames, "05-setup.css") {
		t.Fatal("05-setup.css is not in the css manifest")
	}

	core := readModule(t, "js", "00-core.js")
	if strings.Contains(core, `"setup"`) {
		t.Fatal("setup became a router screen; it must stay an overlay")
	}

	init := readModule(t, "js", "52-init.js")
	openAt := strings.Index(init, "maybeOpenSetup()")
	renderAt := strings.Index(init, "await renderRoute()")
	if openAt < 0 || renderAt < 0 || openAt > renderAt {
		t.Fatal("the boot sequence must open setup before the first route paints")
	}

	setup := readModule(t, "js", "05-setup.js")
	for _, route := range []string{
		`"/api/setup"`,
		`"/api/mailboxes/discover"`,
		`"/api/mailboxes/pick"`,
		`"/api/settings/ui.setup.done"`,
	} {
		if !strings.Contains(setup, route) {
			t.Errorf("setup does not use the existing owner %s", route)
		}
	}
	if !strings.Contains(setup, `!view || !view.needed`) {
		t.Error("setup must open only when the backend says it is needed")
	}
}

// setupList reads one string literal list from the module. The list is the
// owner of a manifest, so the test never restates its entries.
func setupList(t *testing.T, body, name string) []string {
	t.Helper()
	match := regexp.MustCompile(`const ` + regexp.QuoteMeta(name) + ` = \[([^\]]*)\]`).FindStringSubmatch(body)
	if match == nil {
		t.Fatalf("%s is not declared as a literal list", name)
	}
	var out []string
	for _, item := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(match[1], -1) {
		out = append(out, item[1])
	}
	return out
}

// TestSetupStepManifestMatchesTheRenderedSteps keeps the visible sequence and
// the renderer table in one order. Setup stays an overlay over the shell.
func TestSetupStepManifestMatchesTheRenderedSteps(t *testing.T) {
	setup := readModule(t, "js", "05-setup.js")
	steps := setupList(t, setup, "SETUP_STEPS")
	want := []string{"welcome", "mailbox", "model", "finish"}
	if strings.Join(steps, ",") != strings.Join(want, ",") {
		t.Fatalf("SETUP_STEPS = %v, want %v", steps, want)
	}
	for _, name := range steps {
		if !strings.Contains(setup, "function "+name+"Step(") {
			t.Errorf("step %q has no %sStep renderer", name, name)
		}
		dispatch := `state.setupStep === "` + name + `" ? ` + name + "Step("
		if name == "welcome" {
			dispatch = ": " + name + "Step(view)"
		}
		if !strings.Contains(setup, dispatch) {
			t.Errorf("step %q is not dispatched by paintSetup", name)
		}
		if !strings.Contains(setup, `t("setup.step." + name)`) {
			t.Error("the step list must read its labels from the catalog")
		}
	}
	if !strings.Contains(setup, `id: "setup-step-title"`) {
		t.Error("every step needs the focusable step heading")
	}
}

// TestSetupHasBasicAndAdvancedModes protects the two paths: the basic path
// stays four steps, and advanced adds the four groups without becoming a
// second settings screen.
func TestSetupHasBasicAndAdvancedModes(t *testing.T) {
	setup := readModule(t, "js", "05-setup.js")
	modes := setupList(t, setup, "SETUP_MODES")
	if strings.Join(modes, ",") != "basic,advanced" {
		t.Fatalf("SETUP_MODES = %v, want basic and advanced", modes)
	}
	if !strings.Contains(setup, `state.setupMode === "advanced" ? advancedSections() : null`) {
		t.Error("advanced must be an explicit expansion of the welcome step")
	}
	if !strings.Contains(setup, `"aria-pressed":`) && !strings.Contains(setup, `pressed:`) {
		t.Error("the mode switch must expose its pressed state")
	}
	for _, group := range []string{"providers", "runtime", "storage", "network"} {
		if !strings.Contains(setup, `setupAdvancedGroup("`+group+`"`) {
			t.Errorf("advanced path lacks the %s group", group)
		}
	}
	// The two advanced actions reuse the routes Settings uses.
	if !strings.Contains(setup, `"/api/providers/" + encodeURIComponent(record.id) + "/test"`) {
		t.Error("the provider test must use the providers test route")
	}
	if !strings.Contains(setup, `"/api/sidecar/install"`) {
		t.Error("the runtime install must use the sidecar install route")
	}
	if !strings.Contains(setup, `leaveSetup("settings", { tab: "providers" })`) {
		t.Error("the provider group must link to the Providers tab")
	}
	if !strings.Contains(setup, `leaveSetup("settings", { tab: "runtime" })`) {
		t.Error("the runtime group must link to the Runtime tab")
	}
	if !strings.Contains(setup, `settingValueText(view.value)`) || !strings.Contains(setup, `settingEnvName(view.key)`) {
		t.Error("storage and network facts must reuse the settings readers, not restate them")
	}
}

// TestSetupPrefetchesEveryFactOnOpen keeps the reads ahead of the owner. One
// table owns the routes, and every read starts with the overlay, not a step.
func TestSetupPrefetchesEveryFactOnOpen(t *testing.T) {
	setup := readModule(t, "js", "05-setup.js")
	routes := map[string]string{
		"status":    `"/api/status"`,
		"mailboxes": `"/api/mailboxes"`,
		"providers": `"/api/providers"`,
		"sidecar":   `"/api/sidecar"`,
		"learning":  `"/api/learning"`,
		"removals":  `"/api/held-removals"`,
		"settings":  `"/api/settings?keys="`,
	}
	for name, route := range routes {
		entry := regexp.MustCompile(`\b` + name + `:\s*\(\)\s*=>\s*request\(` + regexp.QuoteMeta(route)).FindString(setup)
		if entry == "" {
			t.Errorf("SETUP_READS has no %s read through %s", name, route)
		}
	}
	open := regexp.MustCompile(`function openSetup\(\)[\s\S]*?startSetupReads\(\)`).FindString(setup)
	if open == "" {
		t.Fatal("the pre-fetch must start when the overlay opens")
	}
	if strings.Count(setup, "startSetupReads();") != 1 {
		t.Error("the pre-fetch must have one start site")
	}
	// Database size and the directory are projected by /api/setup, so the
	// overlay never guesses either fact.
	if !strings.Contains(setup, "view.database_size_known") || !strings.Contains(setup, "view.data_dir") {
		t.Error("the overlay must read the data directory and database size from /api/setup")
	}
	// Detected facts come from the platform, never from a literal.
	for _, fragment := range []string{
		"function browserZone()",
		"function browserLanguage()",
		"view.time_zone || browserZone()",
		"other = boxes.length - selected",
	} {
		if !strings.Contains(setup, fragment) {
			t.Errorf("the pre-fill lacks %q", fragment)
		}
	}
}

// TestSetupReadFailureIsIndependent protects the degradation path: one failed
// read keeps its own error and Retry, and the rest of the step keeps painting.
func TestSetupReadFailureIsIndependent(t *testing.T) {
	setup := readModule(t, "js", "05-setup.js")
	if strings.Contains(setup, "Promise.all") || strings.Contains(setup, "Promise.allSettled") {
		t.Error("the reads must settle independently, not as one batch")
	}
	if !strings.Contains(setup, "read.state = \"error\"") {
		t.Error("a read must keep its own error state")
	}
	if !strings.Contains(setup, "read.error = error") {
		t.Error("a read must keep its own error object")
	}
	if !strings.Contains(setup, "function retrySetupRead(name)") {
		t.Error("a failed read needs its own Retry")
	}
	if !strings.Contains(setup, "retryBanner(apiFailure(read.error).message") {
		t.Error("the failure list must offer Retry with the read's own message")
	}
	if !strings.Contains(setup, `t("setup.read." + name)`) {
		t.Error("a failed read must name what is missing")
	}
	if !strings.Contains(setup, `t("setup.not_available")`) {
		t.Error("a failed fact must say it is unavailable")
	}
}

// TestSetupWritesTheMarkerOnce protects the one completion fact.
func TestSetupWritesTheMarkerOnce(t *testing.T) {
	setup := readModule(t, "js", "05-setup.js")
	if count := strings.Count(setup, `"/api/settings/ui.setup.done"`); count != 1 {
		t.Fatalf("the setup marker has %d write sites, want 1", count)
	}
	if !strings.Contains(setup, "if (state.setupCompleting) return;") {
		t.Error("completeSetup must refuse a second marker write")
	}
	if strings.Count(setup, "method: \"PUT\", body: { value: true }") != 1 {
		t.Error("the marker must be written with one settings PUT")
	}
	if !strings.Contains(setup, "clearSetupProgress()") {
		t.Error("a finished setup must clear its progress, not its marker")
	}
}

// TestSetupResumesAtTheSavedStep protects the reload path and the safety of
// Escape: progress is UI state, the marker is the only completion fact.
func TestSetupResumesAtTheSavedStep(t *testing.T) {
	setup := readModule(t, "js", "05-setup.js")
	for _, fragment := range []string{
		`const SETUP_PROGRESS_KEY = "mailsense.setup.step"`,
		"function savedSetupStep()",
		"state.setupStep = savedSetupStep()",
		"saveSetupProgress(name)",
	} {
		if !strings.Contains(setup, fragment) {
			t.Errorf("setup does not resume through %q", fragment)
		}
	}
	if !strings.Contains(setup, "SETUP_STEPS.indexOf(name) > SETUP_STEPS.indexOf(current)") {
		t.Error("a back navigation must not lower the furthest completed step")
	}
	if !strings.Contains(setup, "if (state.setupBusy || state.setupCompleting) return;") {
		t.Error("Escape must stay open while a setup write is in flight")
	}
}

// TestSetupDialogOwnsFocus protects the dialog contract: a real dialog role,
// focus on the step heading, a Tab trap, and no focus in the hidden shell.
func TestSetupDialogOwnsFocus(t *testing.T) {
	setup := readModule(t, "js", "05-setup.js")
	for _, fragment := range []string{
		`role: "dialog"`,
		`"aria-modal": "true"`,
		`tabIndex: -1`,
		"function focusSetupHeading()",
		"document.addEventListener(\"keydown\", onSetupKeydown)",
		"document.addEventListener(\"focusin\", onSetupFocusIn)",
		"if (!root.contains(event.target)) focusSetupHeading();",
	} {
		if !strings.Contains(setup, fragment) {
			t.Errorf("the dialog focus contract lacks %q", fragment)
		}
	}
}

// TestSetupCatalogCoversEveryKey keeps the overlay translatable. The dynamic
// prefixes are checked against the literals the module itself uses, so a new
// step, mode, read or advanced group cannot ship without both catalogs.
func TestSetupCatalogCoversEveryKey(t *testing.T) {
	setup := readModule(t, "js", "05-setup.js")
	used := map[string]bool{}
	for _, match := range regexp.MustCompile(`t\("(setup\.[^"]+)"\)`).FindAllStringSubmatch(setup, -1) {
		if !strings.HasSuffix(match[1], ".") {
			used[match[1]] = true
		}
	}
	for _, name := range setupList(t, setup, "SETUP_STEPS") {
		used["setup.step."+name] = true
	}
	for _, name := range setupList(t, setup, "SETUP_MODES") {
		used["setup.mode."+name] = true
	}
	for _, name := range []string{"auto", "en", "fr"} {
		used["setup.language."+name] = true
	}
	for name := range map[string]bool{
		"status": true, "mailboxes": true, "providers": true, "sidecar": true,
		"learning": true, "removals": true, "settings": true,
	} {
		used["setup.read."+name] = true
	}
	for _, group := range []string{"providers", "runtime", "storage", "network"} {
		used["setup.advanced."+group+".title"] = true
		used["setup.advanced."+group+".sub"] = true
	}
	if len(used) == 0 {
		t.Fatal("no setup keys were found in the module")
	}
	for _, language := range []string{"en", "fr"} {
		catalog := readCatalog(t, "i18n."+language+".js")
		for key := range used {
			if _, ok := catalog[key]; !ok {
				t.Errorf("i18n.%s.js is missing %s", language, key)
			}
		}
	}
}

func containsName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

func readModule(t *testing.T, dir, name string) string {
	t.Helper()
	body, err := fs.ReadFile(Files, path.Join(dir, name))
	if err != nil {
		t.Fatalf("%s/%s: %v", dir, name, err)
	}
	return string(body)
}
