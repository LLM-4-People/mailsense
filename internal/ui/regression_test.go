package ui

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/priority"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
)

func readUIModule(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("js", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func readUICSS(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("css", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// A destructive dialog's confirmation is single flight: the button is
// disabled and the form submit path is inert while onConfirm is pending, so
// a double click cannot start two judge runs or two deletes.
func TestDialogConfirmIsSingleFlight(t *testing.T) {
	js := readUIModule(t, "51-actions.js")
	for _, want := range []string{
		"let busy = false;",
		"if (busy) return;",
		`dom.modal.setAttribute("aria-busy", "true")`,
		"confirm.disabled = true;",
		"cancel.disabled = true;",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the dialog confirmation lacks %q", want)
		}
	}
}

// The first-run zone confirmation is single flight and one shot: the first
// call owns the POST and its one success toast, a concurrent call shares the
// promise, and a later call receives the settled one. A reload that still
// reports the blocker therefore cannot post or toast a second time.
func TestAutoConfirmZoneRunsOncePerPage(t *testing.T) {
	js := readUIModule(t, "51-actions.js")
	for _, want := range []string{
		"let zoneAutoPromise = null;",
		"if (zoneAutoPromise) return zoneAutoPromise;",
		"zoneAutoPromise = (async () => {",
		"})().catch(() => false);",
		`toast(t("learning.zone_auto"`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("autoConfirmZone lacks %q", want)
		}
	}
	body := functionBody(t, js, "function autoConfirmZone() {")
	if got := strings.Count(body, "zoneAutoPromise ="); got != 1 {
		t.Errorf("autoConfirmZone assigns the shared promise %d times, want 1", got)
	}
	if got := strings.Count(body, "toast("); got != 1 {
		t.Errorf("the automatic zone path emits %d toasts, want its one success toast", got)
	}
	for _, forbidden := range []string{"setTimeout(", "setInterval("} {
		if strings.Contains(body, forbidden) {
			t.Errorf("the automatic zone path retries through %q", forbidden)
		}
	}
}

// The sources row has one unpick control: the checkbox delegates to the
// confirmed canonical action instead of posting around it.
func TestSourcesHasOneUnpickOwner(t *testing.T) {
	js := readUIModule(t, "25-sources.js")
	if strings.Contains(js, `"/api/mailboxes/unpick"`) {
		t.Fatal("the sources screen still posts unpick around the canonical action")
	}
	if !strings.Contains(js, "unpickMailbox(box.key") {
		t.Fatal("the mailbox checkbox does not use the canonical unpick action")
	}
	if strings.Contains(js, "mailboxActionCellNode") {
		t.Fatal("the sources table still has a second unpick control")
	}
}

func TestDeclareDialogBindsItsFieldError(t *testing.T) {
	js := readUIModule(t, "25-sources.js")
	for _, want := range []string{
		`markFieldError(input, errorLine, t("sources.declare.required"))`,
		`markFieldError(input, errorLine, t("sources.declare.invalid"))`,
		"input.checkValidity()",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the declare dialog lacks %q", want)
		}
	}
}

func TestAskComposerBindsItsFieldError(t *testing.T) {
	js := readUIModule(t, "22-ask.js")
	if !strings.Contains(js, `markFieldError(input, errorLine, t("ask.error.short"))`) {
		t.Fatal("the Ask composer does not bind its short-input error")
	}
	if strings.Contains(js, `errorLine.textContent = t("ask.error.short")`) {
		t.Fatal("the Ask composer still writes the error without marking the field")
	}
}

func TestCompaniesPeopleTableFitsPhoneWidth(t *testing.T) {
	css := readUICSS(t, "80-companies.css")
	if strings.Contains(css, "min-width: 560px") {
		t.Fatal("the people table still forces a 560px minimum")
	}
	if !strings.Contains(css, "overflow-wrap: anywhere") {
		t.Fatal("the people table cells cannot wrap long addresses")
	}
}

func TestSidebarShutdownKeepsItsAccessibleName(t *testing.T) {
	css := readUICSS(t, "90-responsive.css")
	start := strings.Index(css, ".sidebar-foot .button span")
	if start < 0 {
		t.Fatal("the narrow sidebar label rule is missing")
	}
	end := strings.Index(css[start:], "}")
	if end < 0 {
		t.Fatal("the narrow sidebar label rule has no end")
	}
	rule := css[start : start+end]
	if strings.Contains(rule, "display: none") {
		t.Fatal("the narrow sidebar still removes the shutdown label from the accessibility tree")
	}
	if !strings.Contains(rule, "clip: rect(0, 0, 0, 0)") {
		t.Fatal("the narrow sidebar label is not visually hidden")
	}
}

// A toast must not create a second live region inside #toasts, and a
// reader hovering or focusing it must keep it on screen.
func TestToastUsesTheOneLiveRegion(t *testing.T) {
	js := readUIModule(t, "11-http.js")
	if strings.Contains(js, `role: kind === "error" ? "alert" : "status"`) {
		t.Fatal("the toast adds a nested live region")
	}
	for _, want := range []string{"const dismiss", "mouseenter", "mouseleave", "focusin", "focusout"} {
		if !strings.Contains(js, want) {
			t.Errorf("the toast dismissal lacks %q", want)
		}
	}
}

func TestAskHasAWorkspaceHeadingAndNamedHistory(t *testing.T) {
	js := readUIModule(t, "22-ask.js")
	for _, want := range []string{
		`h("h2", { class: "sr-only", text: t("ask.workspace.title") })`,
		`"aria-label": t("ask.history.title")`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the Ask structure lacks %q", want)
		}
	}
}

// The search screen asks the server for every filter and renders the
// returned page as-is: filtering one default page in the client hid matching
// mail beyond it and could show a wrong count.
func TestSearchFiltersAreSentToTheServer(t *testing.T) {
	js := readUIModule(t, "33-search.js")
	for _, want := range []string{
		`if (sender) params.push("sender=" + encodeURIComponent(sender));`,
		`if (store) params.push("store=" + encodeURIComponent(store));`,
		`if (role) params.push("role=" + encodeURIComponent(role));`,
		`if (attach) params.push("attach=" + encodeURIComponent(attach));`,
		`requestCached(searchPath(), {}, CACHE_TTL.search)`,
		`fmtCount(hits.length, Number(result.total) || 0, !!result.capped)`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the search request lacks %q", want)
		}
	}
	if strings.Contains(js, "hits.filter(") {
		t.Fatal("the search screen filters the server page client-side")
	}
}

// The Logs screen never names a page size: log.ring.size is the server's
// valid maximum, and a hardcoded 500 makes the screen fail below it.
func TestLogsPageSizeIsServerOwned(t *testing.T) {
	js := readUIModule(t, "50-logs.js")
	if strings.Contains(js, "limit=") {
		t.Fatal("the Logs client still sends a hardcoded page size")
	}
}

// The pre-gate label read names the queue page that offered the message:
// the API binds the read to that page, so the flag alone unlocks nothing.
func TestLabelMessageReadNamesItsQueue(t *testing.T) {
	shell := readUIModule(t, "13-shell.js")
	for _, want := range []string{
		`const query = ["for=label"];`,
		`if (options.question) query.push("question=" + encodeURIComponent(options.question));`,
		`if (options.limit) query.push("limit=" + encodeURIComponent(String(options.limit)));`,
		`if (options.review) query.push("review=true");`,
	} {
		if !strings.Contains(shell, want) {
			t.Errorf("the label message path lacks %q", want)
		}
	}
	labels := readUIModule(t, "26-labels.js")
	if !strings.Contains(labels, `loadMessage(task.message_id, { forLabel: true, question: task.question, limit: queueLimit, review })`) {
		t.Fatal("the label screen reads a message without naming the queue that offered it")
	}
}

// Permission has one owner: the shell's gate snapshot. A screen that reads
// state.learning for a gate pill or a lock guard can disagree with the next.
func TestGateSnapshotIsTheOnlyPermissionOwner(t *testing.T) {
	shell := readUIModule(t, "13-shell.js")
	for _, want := range []string{
		`request("/api/gate")`,
		`if (gateInflight) return gateInflight;`,
		`state.gate = {`,
		`function gateOpen()`,
		`function gatePreview()`,
		`function gateAllows()`,
		`function gateKnown()`,
	} {
		if !strings.Contains(shell, want) {
			t.Errorf("the gate owner lacks %q", want)
		}
	}
	for _, name := range []string{"20-hub.js", "21-brief.js", "22-ask.js", "24-companies.js",
		"25-sources.js", "26-labels.js", "27-answers.js", "32-mail.js", "33-search.js", "12-router.js"} {
		js := readUIModule(t, name)
		for _, banned := range []string{
			"gatePill(!!(state.learning && state.learning.open))",
			"state.learning && state.learning.preview && !state.learning.open",
			"state.learning && !state.learning.open && !state.learning.preview",
		} {
			if strings.Contains(js, banned) {
				t.Errorf("%s still derives permission from learning: %q", name, banned)
			}
		}
	}
}

// The two pre-gate message surfaces are explicit and disjoint: the label
// queue read and the stored-answer read. A missing binding is a lock error.
func TestPreGateMessageSurfacesAreExplicit(t *testing.T) {
	shell := readUIModule(t, "13-shell.js")
	if !strings.Contains(shell, `if (options.forAnswer) return "/api/messages/" + encodeURIComponent(id) + "?for=answer";`) {
		t.Error("messagePath has no explicit answer surface")
	}
	answers := readUIModule(t, "27-answers.js")
	if !strings.Contains(answers, `loadMessage(id, { forAnswer: true })`) {
		t.Error("the Answers detail does not name its answer surface")
	}
	if strings.Contains(answers, `loadMessage(id, { forLabel: true })`) {
		t.Error("the Answers detail still asks for the label surface")
	}
}

// A plural count must reach t() as a number: interpolate formats it with the
// UI locale, while Intl.PluralRules needs the numeric value to select the
// French form. Passing formatNumber() makes every count pluralize as zero.
func TestPluralCountsStayNumeric(t *testing.T) {
	entries, err := fs.ReadDir(Files, "js")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".js") {
			continue
		}
		js := readUIModule(t, entry.Name())
		if strings.Contains(js, "count: formatNumber(") {
			t.Errorf("%s passes a preformatted count to t(); pass the raw number", entry.Name())
		}
	}
}

// The Companies search filters the whole ranking on the server and the count
// names the whole population, so a page is never presented as the list.
func TestCompaniesSearchIsServerSide(t *testing.T) {
	shell := readUIModule(t, "13-shell.js")
	if !strings.Contains(shell, `(query ? "&q=" + encodeURIComponent(query) : "")`) {
		t.Error("loadCompanies does not send the search query")
	}
	companies := readUIModule(t, "24-companies.js")
	for _, want := range []string{
		`view = await loadCompanies(query);`,
		`const listTotal = Number(view && view.total) || companies.length;`,
		`state.companiesCount = listTotal;`,
		`navigate("companies", { key: selectedKey, q: value || undefined });`,
		`t("companies.list.truncated"`,
	} {
		if !strings.Contains(companies, want) {
			t.Errorf("the Companies screen lacks %q", want)
		}
	}
}

// Producer progress sentences are English diagnostics. The job chip and the
// Sources meter show localized structure; only the lang="en" sentence
// carries the producer's own wording.
func TestJobProseStaysDiagnostic(t *testing.T) {
	shell := readUIModule(t, "13-jobs.js")
	if strings.Contains(shell, `|| progress.message || ""`) {
		t.Error("the job chip still falls back to the producer's English sentence")
	}
	if !strings.Contains(shell, `t("shell.job.running")`) {
		t.Error("the job chip has no localized running state")
	}
	sources := readUIModule(t, "25-sources.js")
	if strings.Contains(sources,
		`progress && progress.message ? h("p", { class: "fine", lang: "en", dataset: { backfillMessage: "" }, text: progress.message }) : null,`) {
		t.Error("the Sources backfill still renders the producer sentence as main copy")
	}
	if !strings.Contains(sources, `diagnosticDetails(progress.message, { summary: "sources.backfill.details",`) ||
		!strings.Contains(sources, `dataset: { backfillMessage: "" }`) {
		t.Error("the Sources backfill does not use the one English diagnostic")
	}
	components := readUIModule(t, "01-components.js")
	if !strings.Contains(components, `h("details", { class: "blocker-diagnostic", lang: "en" }`) {
		t.Error("the one diagnostic disclosure does not stamp the English language")
	}
}

// Identifiers are not counts, clocks use the owner zone, enum values and
// search modes are translated, and a modal owns the keyboard.
func TestUXConsistencyOwners(t *testing.T) {
	body, err := os.ReadFile("i18n.js")
	if err != nil {
		t.Fatal(err)
	}
	i18n := string(body)
	if !strings.Contains(i18n, `function formatId(value)`) ||
		strings.Contains(i18n, `function formatTime(value)`) {
		t.Error("the identifier/clock formatter owners are wrong")
	}
	if !strings.Contains(i18n, `const zone = zoneFor("owner");`) {
		t.Error("the clock does not use the owner zone")
	}
	search := readUIModule(t, "33-search.js")
	if !strings.Contains(search, `if (state.modalOpen || document.querySelector("dialog[open]")) return;`) {
		t.Error("the shell shortcut is not modal-safe")
	}
	if !strings.Contains(search, `function searchTermModeLabel(mode)`) ||
		!strings.Contains(search, `searchTermModeLabel(term.mode)`) {
		t.Error("search term modes are not translated")
	}
	settings := readUIModule(t, "40-settings.js")
	if !strings.Contains(settings, `const key = "settings.enum." + view.key + "." + option;`) {
		t.Error("settings enum values have no translation owner")
	}
	for _, name := range []string{"i18n.en.js", "i18n.fr.js"} {
		catalog := readCatalog(t, name)
		for _, key := range []string{
			"settings.enum.log.level.warn", "settings.enum.store.synchronous.extra",
			"search.term_mode.substring",
		} {
			if _, ok := catalog[key]; !ok {
				t.Errorf("%s lacks %s", name, key)
			}
		}
	}
}

// A background repaint is not a navigation: it must not move focus out of
// the shell search or a dialog. Only an explicit navigation restores focus.
func TestBackgroundPaintKeepsFocus(t *testing.T) {
	router := readUIModule(t, "12-router.js")
	if !strings.Contains(router, `function appendMain(node, scroll = 0, scrolls = null, restoreFocus = true, focusCarry = null)`) {
		t.Error("appendMain has no focus-restore owner")
	}
	if !strings.Contains(router, `appendMain(node, 0, scrolls, false);`) {
		t.Error("a background repaint still restores focus")
	}
}

// A live refresh must not turn a usable screen into an error, must not hide
// work another refresh still runs, must not leave a phantom job, and must not
// move the reading position or rebuild a log table once per keystroke.
func TestLiveRefreshStability(t *testing.T) {
	router := readUIModule(t, "12-router.js")
	if !strings.Contains(router, `if (isErrorScreen(node) && !isErrorScreen(dom.main.firstElementChild))`) {
		t.Error("a background refresh can still replace a usable screen with an error")
	}
	if strings.Contains(router, `beginBackgroundUpdate`) {
		t.Error("a cached revalidation still owns the shared updating indicator")
	}
	shell := readUIModule(t, "13-jobs.js")
	if !strings.Contains(shell, `const present = new Set(list.map((info) => String(info && info.id ? info.id : "")));`) {
		t.Error("the job snapshot does not prune jobs that finished while offline")
	}
	logs := readUIModule(t, "50-logs.js")
	if !strings.Contains(logs, `if (filterTimer) window.clearTimeout(filterTimer);`) {
		t.Error("the logs filter is not a trailing debounce")
	}
	mail := readUIModule(t, "32-mail.js")
	if !strings.Contains(mail, `if (scroll > 0) list.scrollTop = scroll;`) {
		t.Error("a conversation repaint does not restore the list position")
	}
}

// jsConstList reads one string-array literal from a UI module. The arrays
// are the UI's presentation order; their values are the Go owners'.
func jsConstList(t *testing.T, js, name string) []string {
	t.Helper()
	match := regexp.MustCompile(`const ` + name + ` = \[([^\]]*)\]`).FindStringSubmatch(js)
	if match == nil {
		t.Fatalf("%s not found", name)
	}
	var out []string
	for _, part := range strings.Split(match[1], ",") {
		part = strings.Trim(strings.TrimSpace(part), `"`)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// The UI keeps small ordered mirrors for chips, filters and badges. Their
// values are owned by mail, priority and threadintel; this contract fails the
// moment a new role, filter or state is added on one side only.
func TestSharedTaxonomiesMatchTheirOwners(t *testing.T) {
	components := readUIModule(t, "01-components.js")
	mailJS := readUIModule(t, "32-mail.js")

	roles := []string{"all"}
	for _, role := range mail.Roles() {
		roles = append(roles, string(role))
	}
	if got := jsConstList(t, mailJS, "MAIL_ROLES"); !slices.Equal(got, roles) {
		t.Fatalf("MAIL_ROLES = %v, owner = %v", got, roles)
	}

	filters := make([]string, 0, len(priority.Filters))
	for _, filter := range priority.Filters {
		filters = append(filters, string(filter))
	}
	if got := jsConstList(t, mailJS, "PRIORITY_FILTERS"); !slices.Equal(got, filters) {
		t.Fatalf("PRIORITY_FILTERS = %v, owner = %v", got, filters)
	}

	states := make([]string, 0, len(threadintel.States))
	for _, state := range threadintel.States {
		states = append(states, string(state))
	}
	if got := jsConstList(t, components, "THREAD_STATES"); !slices.Equal(got, states) {
		t.Fatalf("THREAD_STATES = %v, owner = %v", got, states)
	}
}

// A failed read keeps its API status and retry surface. A boolean flag or a
// static sentence hides the actionable cause behind a generic message.
func TestFailedReadsPreserveStatus(t *testing.T) {
	cases := []struct{ file, needle string }{
		{"23-ask-history.js", "apiFailure(error).message"},
		{"27-answers.js", "apiFailure(error).message"},
		{"21-brief.js", "errorState(error, () => refreshActiveRoute()"},
		{"41-providers.js", "apiFailure(providers.error).message"},
	}
	for _, c := range cases {
		if !strings.Contains(readUIModule(t, c.file), c.needle) {
			t.Errorf("%s does not preserve the failed read's status (%q)", c.file, c.needle)
		}
	}
}

// The shell keeps one in-flight gate read, drops the questions copy with the
// questions cache, narrows message invalidation to the side that changed, and
// does not parse live log lines while Logs is not mounted.
func TestFrontendAPIHygiene(t *testing.T) {
	shell := readUIModule(t, "13-shell.js")
	for _, want := range []string{
		"let gateInflight = null;",
		"if (gateInflight) return gateInflight;",
		`"/api/messages?"`,
		`"/api/messages/"`,
		`function handleLogWake(event)`,
		`if (state.route !== "logs") return;`,
		"state.questions = null;",
	} {
		if !strings.Contains(shell, want) {
			t.Errorf("13-shell.js lacks %q", want)
		}
	}
}

// The certified class is the server's: the UI must not re-threshold a noul
// value at 0.5 or round an expected score level into a different class.
func TestTriageReadingsUseTheCertifiedClass(t *testing.T) {
	search := readUIModule(t, "33-search.js")
	for _, want := range []string{
		`const decided = String(value.decided || "").trim().toLowerCase();`,
		`const decidedLevel = /^\d+$/.test(decidedText) ? Number(decidedText) : null;`,
	} {
		if !strings.Contains(search, want) {
			t.Errorf("33-search.js lacks %q", want)
		}
	}
	if strings.Contains(search, "const yes = probability >= 0.5;") {
		t.Error("the noul reading still re-thresholds the model value")
	}
}

// UX recovery and selection have one owner each: a retry goes through the
// router, the two clear actions differ, the open row is announced, a busy
// dialog ignores its X, and a live refresh restores inside-main focus only.
func TestUXRecoveryAndSelectionContracts(t *testing.T) {
	mail := readUIModule(t, "32-mail.js")
	for _, want := range []string{
		`return errorState(error, () => refreshActiveRoute());`,
		`row.removeAttribute("aria-current");`,
		`selected.setAttribute("aria-current", "true");`,
	} {
		if !strings.Contains(mail, want) {
			t.Errorf("32-mail.js lacks %q", want)
		}
	}
	search := readUIModule(t, "33-search.js")
	for _, want := range []string{
		`const clearSender = () => navigate("mail", keepFilters());`,
		`const clearFilters = () => navigate("mail", { mode: "search", q });`,
		// The chip markup is one component; the screen owns only what
		// clearing its sender means (keep the query and the other filters).
		`senderChip(sender, clearSender)`,
	} {
		if !strings.Contains(search, want) {
			t.Errorf("33-search.js lacks %q", want)
		}
	}
	sources := readUIModule(t, "25-sources.js")
	for _, want := range []string{
		`discoverSelect.addEventListener("change", () => navigate("sources", { source: discoverSelect.value }));`,
		`htmlBackfillSection(htmlJob, selectedSource, jobsRead.error),`,
		`body: { source: sourceID || "" }`,
	} {
		if !strings.Contains(sources, want) {
			t.Errorf("25-sources.js lacks %q", want)
		}
	}
	if !strings.Contains(readUIModule(t, "01-components.js"), `if (opts.selected) attrs["aria-current"] = "true";`) {
		t.Error("mailRow does not announce the selected row")
	}
	if !strings.Contains(readUIModule(t, "51-actions.js"), `if (closeButton) closeButton.onclick = () => { if (!busy) close(false); };`) {
		t.Error("the modal X ignores the busy guard")
	}
	router := readUIModule(t, "12-router.js")
	if !strings.Contains(router, "appendMain(node, 0, scrolls, true);") {
		t.Error("a live refresh drops focus inside #main")
	}
}

// A save that rebuilds its row keeps the keyboard focus in that row, and a
// partial provider-role save invalidates what the app serves and says which
// roles were stored.
func TestSettingsAndProviderSavesStayHonest(t *testing.T) {
	settings := readUIModule(t, "40-settings.js")
	for _, want := range []string{
		`const hadFocus = row.contains(event.currentTarget);`,
		`if (hadFocus) {`,
		`const target = next.querySelector(".setting-actions button, input, select");`,
	} {
		if !strings.Contains(settings, want) {
			t.Errorf("40-settings.js lacks %q", want)
		}
	}
	providers := readUIModule(t, "41-providers.js")
	for _, want := range []string{
		`if (saved > 0) invalidateProviderChange();`,
		`t("settings.providers.error.role_partial", {`,
	} {
		if !strings.Contains(providers, want) {
			t.Errorf("41-providers.js lacks %q", want)
		}
	}
}

// Pause, Clear and the disclosure all describe the view they own: the glyph
// follows the label, Clear resets the ring meta, and aria-controls names a
// row only while that row exists.
func TestLogsControlsTellTheTruth(t *testing.T) {
	logs := readUIModule(t, "50-logs.js")
	for _, want := range []string{
		`glyph.replaceWith(icon(state.logPaused ? "play" : "pause", "small"));`,
		`state.logOldest = 0;`,
		`state.logLast = 0;`,
		`if (expanded) toggle.setAttribute("aria-controls", "log-attrs-" + entry.seq);`,
		`else toggle.removeAttribute("aria-controls");`,
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("50-logs.js lacks %q", want)
		}
	}
}

// A stale Brief says it is updating while it revalidates, and Mark seen
// announces the stored boundary instead of silently repainting.
func TestBriefStaleStateAndMarkSeenAreHonest(t *testing.T) {
	brief := readUIModule(t, "21-brief.js")
	for _, want := range []string{
		`const revalidating = !!known && !warm;`,
		`text: revalidating ? t("brief.updating") : t("brief.loading")`,
		`async function markBriefSeen()`,
		`toast(t("brief.mark_seen_done"), "info");`,
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("21-brief.js lacks %q", want)
		}
	}
}

// The configured agenda default is the server's. The first read omits the
// horizon, adopts the applied window, and the chips can always name it.
func TestAgendaDefaultIsServerOwned(t *testing.T) {
	core := readUIModule(t, "00-core.js")
	if !strings.Contains(core, "agendaHours: null,") {
		t.Error("the UI invents a default agenda horizon")
	}
	shell := readUIModule(t, "13-shell.js")
	for _, want := range []string{
		`? "/api/agenda"`,
		`: "/api/agenda?hours=" + encodeURIComponent(String(hours))`,
	} {
		if !strings.Contains(shell, want) {
			t.Errorf("13-shell.js lacks %q", want)
		}
	}
	hub := readUIModule(t, "20-hub.js")
	for _, want := range []string{
		`const applied = Number(agendaView && agendaView.horizon_hours);`,
		`if (hours > 0 && !horizons.includes(hours)) horizons.push(hours);`,
	} {
		if !strings.Contains(hub, want) {
			t.Errorf("20-hub.js lacks %q", want)
		}
	}
}

// The method + path cache key is written once. The three synchronous cache
// helpers take the same options as requestCached, so a cached non-GET read
// cannot be stored under one key and looked up under another.
func TestCacheKeyHasOneOwner(t *testing.T) {
	http := readUIModule(t, "11-http.js")
	if strings.Contains(http, `"GET "`) {
		t.Error("a cache helper hardcodes the GET key instead of using cacheKey")
	}
	for _, want := range []string{
		`function cached(path, options = {}) {`,
		`const key = cacheKey(path, options);`,
		`apiCache.get(cacheKey(path, options))`,
		`apiCache.set(cacheKey(path, options), { value, at: Date.now(), ttl });`,
		`const hit = cached(path, options);`,
	} {
		if !strings.Contains(http, want) {
			t.Errorf("11-http.js lacks %q", want)
		}
	}
}

// The search filter and the `in:` token read MAIL_ROLES through one
// projection. A role added to the owner list appears in search automatically,
// and a second literal list cannot drift.
func TestSearchRolesUseTheMailRoleOwner(t *testing.T) {
	mail := readUIModule(t, "32-mail.js")
	if !strings.Contains(mail, `const SEARCH_ROLES = MAIL_ROLES.filter((role) => role !== "all");`) {
		t.Error("32-mail.js has no search-role projection of MAIL_ROLES")
	}
	search := readUIModule(t, "33-search.js")
	for _, banned := range []string{`["", "inbox"`, `in:(inbox|`} {
		if strings.Contains(search, banned) {
			t.Errorf("33-search.js keeps a second role vocabulary: %q", banned)
		}
	}
	for _, want := range []string{
		`const roles = ["", ...SEARCH_ROLES];`,
		`SEARCH_ROLES.includes(role)`,
	} {
		if !strings.Contains(search, want) {
			t.Errorf("33-search.js lacks %q", want)
		}
	}
}

// A 2xx response with a non-empty unreadable body is a failure, not an empty
// success: returning null would cache and paint a failed read as empty. A GET
// whose parsed payload is null is the same failure: an empty message read is
// never a message.
func TestUnreadableSuccessBodyIsAnError(t *testing.T) {
	http := readUIModule(t, "11-http.js")
	for _, want := range []string{
		`throw new ApiError(response.status || 0, { code: "invalid_response" });`,
		`if (method === "GET" && payload === null) {`,
		`throw new ApiError(response.status, { code: "invalid_response" });`,
		`} else if (I18N.has("error." + error.code)) {`,
	} {
		if !strings.Contains(http, want) {
			t.Errorf("11-http.js lacks %q", want)
		}
	}
}

// A removed message is classified once. The transport (http_404/http_410) and
// server (not_found) spellings are the same fact, and the message-reading
// screens use that predicate instead of an empty-shaped view.
func TestRemovedMessageReadHasOneOwner(t *testing.T) {
	http := readUIModule(t, "11-http.js")
	block := markedSource(t, http, "removed-read")
	for _, want := range []string{
		"function removedMessageError(error)",
		`error.status === 404`,
		`error.status === 410`,
		`error.code === "not_found"`,
		`error.code === "http_404"`,
		`error.code === "http_410"`,
	} {
		if !strings.Contains(block, want) {
			t.Errorf("the removed-read owner lacks %q", want)
		}
	}

	search := readUIModule(t, "33-search.js")
	for _, want := range []string{
		"function mailDetailReadState(messageResult, triageResult)",
		"const read = mailDetailReadState(messageResult, triageResult);",
		"removedMessageError(messageResult.reason)",
		"validMessageView(messageResult.value)",
		`t("detail.unavailable")`,
	} {
		if !strings.Contains(search, want) {
			t.Errorf("33-search.js lacks %q", want)
		}
	}
	for _, forbidden := range []string{
		"!messageView && !triage",
		`t("search.detail.unavailable")`,
		"errorScreen(",
	} {
		if strings.Contains(search, forbidden) {
			t.Errorf("the search detail still decides from triage presence: %q", forbidden)
		}
	}

	answers := readUIModule(t, "27-answers.js")
	for _, want := range []string{
		"removedMessageError(error)",
		"validMessageView(view)",
		`t("detail.unavailable")`,
	} {
		if !strings.Contains(answers, want) {
			t.Errorf("27-answers.js lacks %q", want)
		}
	}
	if strings.Contains(answers, "messageResult.value || {}") {
		t.Error("the answers detail turns an empty message read into an empty view")
	}

	conversation := readUIModule(t, "34-conversation.js")
	if !strings.Contains(conversation, `unavailableState(t("detail.unavailable"))`) {
		t.Error("the message detail has no removed/no-message state")
	}
	if strings.Contains(conversation, `t("detail.triage_result")`) {
		t.Error("the message detail still paints a triage result without a message")
	}
}

// Job names, scope verdicts and the sender filter are owner-facing: a raw
// producer name or enum never reaches the primary copy, and a filtered All
// Mail list always shows the filter with a way to clear it.
func TestOwnerFacingJobAndScopeNamesAreLocalized(t *testing.T) {
	jobs := readUIModule(t, "13-jobs.js")
	if !strings.Contains(jobs, `const name = jobDisplayName(info);`) {
		t.Error("the job chip shows the raw producer name")
	}
	if !strings.Contains(jobs, "function jobDisplayName(info)") || !strings.Contains(jobs, "jobNameLabel(info && info.kind") {
		t.Error("jobDisplayName does not translate the producer kind and name")
	}
	if !strings.Contains(readUIModule(t, "23-learning.js"), `jobNameLabel(job.name)`) {
		t.Error("Learning shows raw running-job names")
	}
	if !strings.Contains(readUIModule(t, "34-conversation.js"), `scopeVerdictLabel(messageView.verdict)`) {
		t.Error("the message badge shows the raw scope verdict")
	}
	mail := readUIModule(t, "32-mail.js")
	// The filtered list always shows the active sender with a way to clear it.
	// The markup, its two labels and the clear control belong to the one
	// sender-chip component (TestSenderChipIsOneComponent); the screen passes
	// its own clear action so a cleared filter keeps the rest of the route.
	if !strings.Contains(mail, "senderChip(sender, clearSender)") {
		t.Error("All Mail does not show the active sender as a removable chip")
	}
}

// The settings row already shows its description; the control keeps that
// description as its accessible name without painting it twice, and a save
// keeps the keyboard focus inside the rebuilt row.
func TestSettingsControlLabelsAndFocus(t *testing.T) {
	settings := readUIModule(t, "40-settings.js")
	if strings.Contains(settings, `const label = (option) =>`) {
		t.Error("the enum option label shadows the field label")
	}
	for _, want := range []string{
		`const optionLabel = (option) =>`,
		`const hadFocus = row.contains(event.currentTarget);`,
		`field(label, select, null, { srOnly: true })`,
	} {
		if !strings.Contains(settings, want) {
			t.Errorf("40-settings.js lacks %q", want)
		}
	}
	if !strings.Contains(readUIModule(t, "00-core.js"), `if (options.srOnly) labelNode.className = "sr-only";`) {
		t.Error("field cannot hide a duplicated settings label")
	}
	// The hidden label is absolutely positioned. Without a positioned field
	// its containing block is the initial containing block, and its static
	// position at the bottom of the settings list grows the document scroll
	// even though the list itself stays bounded.
	css := readUICSS(t, "20-components.css")
	if !regexp.MustCompile(`(?m)^\.field\s*\{[^}]*\bposition:\s*relative\b`).MatchString(css) {
		t.Error(".field is not a containing block for its sr-only label")
	}
}
