package ui

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// appendMain clears #main, which destroys the focused node and drops focus to
// <body>; the next Tab then restarts at the page top. The rebuild captures the
// focused row first and restores it, and it never steals focus from outside
// the screen (the global search box or a dialog).
func TestMainRebuildRestoresTheFocusedRow(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	start := strings.Index(js, "function captureMainFocus() {")
	if start < 0 {
		t.Fatal("captureMainFocus not found")
	}
	end := strings.Index(js[start:], "function routeScreen(")
	if end < 0 {
		t.Fatal("routeScreen not found")
	}
	block := js[start : start+end]
	for _, required := range []string{
		"function captureMainFocus()",
		"function restoreMainFocus(previous)",
		"const previousFocus = restoreFocus ? (focusCarry || captureMainFocus()) : null;",
		"restoreMainFocus(previousFocus);",
		"!dom.main.contains(active)",
		"node.focus({ preventScroll: true })",
	} {
		if !strings.Contains(block, required) {
			t.Fatalf("the focus settlement lacks %q", required)
		}
	}
	if got := strings.Count(block, "restoreMainFocus(previousFocus)"); got != 1 {
		t.Fatalf("appendMain restores focus %d times, want once", got)
	}
	// The fallback must target the named landmark, not an unnamed screen.
	if !strings.Contains(block, `dom.main.focus({ preventScroll: true });`) {
		t.Fatal("the fallback focus target is not the named main landmark")
	}
}

// The search input is the combobox; its options are not tab stops, and the
// note rows stay valid listbox children.
func TestGlobalSearchIsOneCombobox(t *testing.T) {
	html, err := fs.ReadFile(Files, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	shell := string(html)
	start := strings.Index(shell, `id="global-search-input"`)
	if start < 0 {
		t.Fatal("global-search-input not found")
	}
	input := shell[start:]
	if end := strings.Index(input, ">"); end >= 0 {
		input = input[:end]
	}
	for _, required := range []string{`role="combobox"`, `aria-controls="global-search-results"`, `aria-autocomplete="list"`, `aria-expanded="false"`} {
		if !strings.Contains(input, required) {
			t.Fatalf("the search input lacks %q", required)
		}
	}

	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	start = strings.Index(js, "function renderGlobalSearchResults(")
	if start < 0 {
		t.Fatal("the global search result renderer not found")
	}
	end := strings.Index(js[start:], "async function loadGlobalSearch(")
	if end < 0 {
		t.Fatal("loadGlobalSearch not found")
	}
	block := js[start : start+end]
	for _, required := range []string{`tabIndex: -1`, `role: "option"`, `"aria-disabled": "true"`} {
		if !strings.Contains(block, required) {
			t.Fatalf("the suggestion list lacks %q", required)
		}
	}
	if strings.Contains(block, `h("button", { type: "button", class: "topbar-suggest`) {
		t.Fatal("a suggestion is still a tab stop")
	}
}

// The log table is one Tab stop: a real disclosure button per row, one
// roving tab stop, and arrow/Home/End movement between rows.
func TestLogTableHasOneRovingTabStop(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	for _, required := range []string{
		`toggle.id = "log-toggle-" + entry.seq`,
		`toggle.tabIndex = state.logFocusSeq === entry.seq ? 0 : -1`,
		`function syncLogTabStops(host)`,
		`function focusLogToggle(seq, step, edge = false)`,
		`node.tabIndex = node === target ? 0 : -1`,
		`toggle.setAttribute("aria-controls", "log-attrs-" + entry.seq)`,
	} {
		if !strings.Contains(js, required) {
			t.Fatalf("the log table lacks %q", required)
		}
	}
	// A <tr> cannot be a button: the disclosure is a real control now.
	if strings.Contains(js, `h("tr", {
      class: "log-row",
      tabIndex: 0,
      role: "button"`) {
		t.Fatal("the log row is still a fake button")
	}
	if !strings.Contains(js, `h("tr", { class: "log-row", dataset: { seq: String(entry.seq) } })`) {
		t.Fatal("the log row lost its row semantics")
	}
}

// Every settings row controls one named setting. A generic "Value" label
// repeated down the page names nothing, so the row's own title names the
// control and the hint is referenced by aria-describedby.
func TestSettingControlsAreNamedByTheirSetting(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("js", "40-settings.js"))
	if err != nil {
		t.Fatal(err)
	}
	js := string(source)
	if strings.Contains(js, `field(t("settings.value")`) {
		t.Fatal("a settings control still uses the generic Value label")
	}
	if !strings.Contains(js, "settingControl(view, status, opts, localizedDescription || key)") {
		t.Fatal("the setting row does not pass its title to the control")
	}
	if got := strings.Count(js, "field(label,"); got != 7 {
		t.Fatalf("%d settings controls use the setting label, want 7", got)
	}
}

func TestFieldHintDescribesItsControl(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("js", "00-core.js"))
	if err != nil {
		t.Fatal(err)
	}
	js := string(source)
	for _, required := range []string{
		`const hintId = control.id + "-hint"`,
		`control.setAttribute("aria-describedby", describedBy ? describedBy + " " + hintId : hintId)`,
		`h("span", { class: "field-hint", id: hintId, text: hint })`,
	} {
		if !strings.Contains(js, required) {
			t.Fatalf("field() lacks %q", required)
		}
	}
}

// A wait is a live status even when focus stays elsewhere; a caller with its
// own live region opts out explicitly.
func TestLoadingStateIsALiveStatusByDefault(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("js", "01-components.js"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), `role: opts.role === undefined ? "status" : (opts.role || null)`) {
		t.Fatal("loadingState does not announce itself by default")
	}
}

// A route placeholder is a silent wait unless it carries a status: the
// skeleton builder owns that announcement once for every route.
func TestEverySkeletonScreenAnnouncesItsWait(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	start := strings.Index(js, "function routeSkeleton(name) {")
	if start < 0 {
		t.Fatal("routeSkeleton not found")
	}
	end := strings.Index(js[start:], "async function renderRouteNode(")
	if end < 0 {
		t.Fatal("renderRouteNode not found")
	}
	block := js[start : start+end]
	if strings.Contains(block, `"aria-busy": "true"`) {
		t.Fatal("a route still builds its own busy wrapper instead of skeletonScreen")
	}
	if got := strings.Count(block, "skeletonScreen("); got != 8 {
		t.Fatalf("routeSkeleton calls skeletonScreen %d times, want 8", got)
	}
	helper := js[strings.Index(js, "function skeletonScreen("):]
	helper = helper[:strings.Index(helper, "\n  }")]
	if !strings.Contains(helper, `role: "status"`) || !strings.Contains(helper, `"aria-busy": "true"`) {
		t.Fatal("skeletonScreen lacks the busy flag or the status announcement")
	}
}

// The segmented controls switch routes, not panels: aria-current is the
// honest state and no element claims a tablist without tabpanels.
func TestRouteSegmentsUseAriaCurrent(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	if strings.Contains(js, `role: "tablist"`) || strings.Contains(js, `role: "tab"`) {
		t.Fatal("a route switch still claims tab semantics without a tabpanel")
	}
	if !strings.Contains(js, `"aria-current": selected ? "page" : null`) {
		t.Fatal("the route segments do not expose aria-current")
	}
	css, err := fs.ReadFile(Files, "app.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), `.segmented button[aria-current="page"]`) {
		t.Fatal("the selected segment style does not follow aria-current")
	}
}

// A disable during an async action must not drop the keyboard user at the
// shell top: the busy owner restores focus only when the trigger had it.
func TestBusyControlsRestoreFocus(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	start := strings.Index(js, "async function withBusy(")
	if start < 0 {
		t.Fatal("withBusy not found")
	}
	end := strings.Index(js[start:], "\n  }")
	if end <= 0 {
		t.Fatal("the end of withBusy was not found")
	}
	body := js[start : start+end]
	for _, want := range []string{
		"document.activeElement === trigger",
		"document.activeElement === document.body",
		"trigger.focus()",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("withBusy lacks %q", want)
		}
	}
}

// The agenda's three horizons are a pressed group, not three unrelated
// buttons whose selection is only a color.
func TestAgendaHorizonsExposePressedState(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	if !strings.Contains(js, `"aria-pressed": opts.pressed === undefined ? null : String(opts.pressed)`) {
		t.Fatal("button has no pressed option")
	}
	if !strings.Contains(js, "pressed: h === hours") {
		t.Fatal("the agenda horizon chips do not expose their selection")
	}
}

// The log table is named and its columns are scoped; the main landmark is
// named by the route title so navigation announces the screen.
func TestLogTableAndMainAreNamed(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	for _, want := range []string{`t("logs.table.caption")`, `scope: "col"`} {
		if !strings.Contains(js, want) {
			t.Errorf("the log table lacks %q", want)
		}
	}
	page, err := fs.ReadFile(Files, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), `aria-labelledby="screen-title"`) {
		t.Fatal("the main landmark is not named by the route title")
	}
}

// A form error is an alert tied to the control it describes, not a floating
// sentence: the control is invalid, the message is in its description, and
// clearing the message removes both.
func TestFieldErrorsAreBoundToTheirControl(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	helper := functionBody(t, js, "function markFieldError(")
	for _, want := range []string{
		`setAttribute("role", "alert")`,
		`setAttribute("aria-invalid", "true")`,
		`removeAttribute("aria-invalid")`,
		`setAttribute("aria-describedby"`,
		`removeAttribute("aria-describedby")`,
	} {
		if !strings.Contains(helper, want) {
			t.Errorf("markFieldError lacks %q", want)
		}
	}
	for _, call := range []string{
		"markFieldError(control.node, error,",
		"markFieldError(input, errorLine,",
	} {
		if !strings.Contains(js, call) {
			t.Errorf("a form does not bind its error to the field: %q", call)
		}
	}
	zone := functionBody(t, js, "function zoneDialog(")
	if !strings.Contains(zone, "markFieldError(input, errorLine,") {
		t.Error("the zone dialog does not bind its error to the input")
	}
	if !strings.Contains(js, `markFieldError(control, errorLine, t("labels.form.answer_all"))`) {
		t.Error("the Labels form error is not bound to the unanswered control")
	}
}

// A warning banner is a live status unless its caller says otherwise, so a
// failed supplementary read is announced without each screen remembering it.
func TestWarningBannerIsALiveStatusByDefault(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("js", "01-components.js"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), `const role = opts.role === undefined ? "status" : opts.role;`) {
		t.Fatal("warningBanner does not announce itself by default")
	}
}

// Every paged list owns the same showing, more, end and error states. The
// pager keeps focus addressable when a page ends.
func TestPagerOwnsTheSharedPagingStates(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("js", "01-components.js"))
	if err != nil {
		t.Fatal(err)
	}
	js := string(source)
	for _, want := range []string{
		"function pagerNode(",
		"function pagerPaint(",
		`class: "pager-showing", role: "status"`,
		`class: "pager-error", role: "alert"`,
		"function pagerFocusShowing(",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the shared pager lacks %q", want)
		}
	}

	router, err := os.ReadFile(filepath.Join("js", "12-router.js"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(router), `"focusKey"`) {
		t.Fatal("the router does not capture a route switch's stable focus key")
	}
}
