package ui

import (
	"regexp"
	"strings"
	"testing"
)

// Every cross-screen destination in the console goes through navigateWith: a
// real anchor the router intercepts, so it keeps the current route state (Back
// returns to where the owner was) and stays in the tab order. These tests read
// the modules and the catalogs, because the guarantees are structural.

// crosslinkModules are the modules this round added cross-screen links to.
var crosslinkModules = []string{
	"23-learning.js",
	"25-sources.js",
	"40-settings.js",
	"41-providers.js",
}

// screenIDs reads the one list of routed screens out of the shell core.
func screenIDs(t *testing.T) []string {
	t.Helper()
	core := readUIModule(t, "00-core.js")
	match := regexp.MustCompile(`const SCREEN_IDS = \[([^\]]+)\]`).FindStringSubmatch(core)
	if match == nil {
		t.Fatal("00-core.js has no SCREEN_IDS declaration")
	}
	var ids []string
	for _, part := range strings.Split(match[1], ",") {
		id := strings.Trim(strings.TrimSpace(part), `"`)
		if id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		t.Fatal("SCREEN_IDS is empty")
	}
	return ids
}

// navigateWithTargets lists the route name of every navigateWith call in one
// module. The first argument is an expression (often a t() call), so the
// scanner balances brackets instead of matching a pattern. A target that is a
// variable (a table row) is reported as empty and checked where the table
// states it.
func navigateWithTargets(t *testing.T, name, source string) []string {
	t.Helper()
	const marker = "navigateWith("
	var out []string
	for offset := 0; ; {
		index := strings.Index(source[offset:], marker)
		if index < 0 {
			return out
		}
		start := offset + index + len(marker)
		depth := 1
		position := start
		for position < len(source) && depth > 0 {
			switch source[position] {
			case '(', '{', '[':
				depth++
			case ')', '}', ']':
				depth--
			}
			if depth == 1 && source[position] == ',' {
				break
			}
			position++
		}
		argument := position + 1
		for argument < len(source) && (source[argument] == ' ' || source[argument] == '\n') {
			argument++
		}
		if argument < len(source) && source[argument] == '"' {
			end := strings.IndexByte(source[argument+1:], '"')
			if end < 0 {
				t.Fatalf("%s: unterminated route literal", name)
			}
			out = append(out, source[argument+1:argument+1+end])
		} else {
			out = append(out, "")
		}
		offset = start
	}
}

// TestCrossLinksTargetRealRoutes: every link names a screen the router knows.
// A typo would otherwise fall back to Home without any error. A link whose
// target is a table row is checked through the table's own screen literal.
func TestCrossLinksTargetRealRoutes(t *testing.T) {
	ids := screenIDs(t)
	known := make(map[string]bool, len(ids))
	for _, id := range ids {
		known[id] = true
	}
	total, dynamic := 0, 0
	for _, name := range crosslinkModules {
		source := readUIModule(t, name)
		targets := navigateWithTargets(t, name, source)
		if len(targets) == 0 {
			t.Errorf("%s has no cross-screen link through navigateWith", name)
		}
		for _, target := range targets {
			if target == "" {
				dynamic++
				continue
			}
			total++
			if !known[target] {
				t.Errorf("%s links to %q, which is not a SCREEN_ID", name, target)
			}
		}
		for _, match := range regexp.MustCompile(`screen: "([a-z]+)"`).FindAllStringSubmatch(source, -1) {
			total++
			if !known[match[1]] {
				t.Errorf("%s routes a table entry to %q, which is not a SCREEN_ID", name, match[1])
			}
		}
	}
	if total == 0 {
		t.Fatal("no cross-screen link was found")
	}
	// The two dynamic targets are the Learning blocker table and the mailbox
	// source constant; both name their screen as a checked literal elsewhere.
	if dynamic != 2 {
		t.Errorf("%d dynamic cross-link targets, want the blocker table and the mailbox source", dynamic)
	}
	mailbox := regexp.MustCompile(`const SETTINGS_MAILBOX_SOURCE = "([a-z]+)"`).FindStringSubmatch(readUIModule(t, "40-settings.js"))
	if mailbox == nil || !known[mailbox[1]] {
		t.Errorf("the mailbox settings link does not name a real screen: %v", mailbox)
	}
}

// TestCrossLinksUseOneHelper: the touched modules build no anchor of their
// own. The runtime panel's notice is the one documented exception: its Node
// harness runs that block alone, so it reads the router's hash builder.
func TestCrossLinksUseOneHelper(t *testing.T) {
	core := readUIModule(t, "13-shell.js")
	for _, want := range []string{
		`function navigateWith(label, name, params = {}, opts = {})`,
		`const anchor = link(label, routeHash(name, params), opts);`,
		`anchor.dataset.crosslink = name;`,
	} {
		if !strings.Contains(core, want) {
			t.Errorf("13-shell.js lacks the shared link helper step %q", want)
		}
	}
	for _, name := range crosslinkModules {
		source := readUIModule(t, name)
		anchors := strings.Count(source, `h("a"`)
		if name == "41-providers.js" {
			if anchors != 1 || !strings.Contains(source, `h("a", { href: routeHash("settings", { tab: "providers" })`) {
				t.Errorf("%s has %d hand-built anchors, want only the runtime notice", name, anchors)
			}
			continue
		}
		if anchors != 0 {
			t.Errorf("%s builds %d anchors instead of the shared link helper", name, anchors)
		}
	}
}

// TestCrossLinkDestinations names the links the audit asked for, with the
// route parameters that land on the row or job they describe.
func TestCrossLinkDestinations(t *testing.T) {
	cases := []struct{ file, want string }{
		// Sources reaches the filters, the failure and the learning it feeds.
		{"25-sources.js", `navigateWith(t("sources.link.sync_settings"), "settings", { tab: "general", q: "sync." }`},
		{"25-sources.js", `navigateWith(t("sources.link.outlook_filters"), "settings", { tab: "general", q: "outlook." }`},
		{"25-sources.js", `navigateWith(t("nav.learning"), "learning", {}`},
		{"25-sources.js", `navigateWith(t("sources.link.failed_job"), "logs", { job: job.id }`},
		// Learning reaches the settings a blocker names and the log of a run.
		{"23-learning.js", `navigateWith(t("logs.open"), "logs", runningJobs.length === 1 ? { job: runningJobs[0].id } : {}`},
		{"23-learning.js", `evidence_run_failed: { screen: "logs", label: "logs.open" }`},
		{"23-learning.js", `layer1_config_invalid: { screen: "settings", label: "learning.link.settings", params: { tab: "general", q: "layer1." } }`},
		{"23-learning.js", `no_embedder: { screen: "settings", label: "learning.link.settings", params: { tab: "runtime" } }`},
		// Settings reaches Sources for the mailbox keys and Logs for log.*.
		{"40-settings.js", `navigateWith(t("settings.managed_in_sources"), SETTINGS_MAILBOX_SOURCE`},
		{"40-settings.js", `key.startsWith("log.") ? navigateWith(t("logs.open"), "logs", {}`},
		// Providers reaches the log of a failed call.
		{"41-providers.js", `navigateWith(t("logs.open"), "logs", { component: "engine" }`},
	}
	for _, tc := range cases {
		if !strings.Contains(readUIModule(t, tc.file), tc.want) {
			t.Errorf("%s lacks the cross link %q", tc.file, tc.want)
		}
	}
}

// TestLogsJobFilterIsARealView: the job link lands on a filter, not on a
// screen that silently drops the id. The route owns the value, the chip clears
// it, and the page asks for the attributes it filters on.
func TestLogsJobFilterIsARealView(t *testing.T) {
	logs := readUIModule(t, "50-logs.js")
	for _, want := range []string{
		`state.logFilters.job = route.params.get("job") || "";`,
		`if (state.logFilters.job) {`,
		`return "/api/logs?attrs=on" + (since > 0 ? "&since=" + String(since) : "");`,
		`if (job && logEntryJob(entry) !== job) return false;`,
		`const attrs = entry && entry.attrs && typeof entry.attrs === "object" ? entry.attrs : {};`,
		`return String(attrs.job || attrs.job_id || "");`,
		`t("logs.filter.job", { job })`,
		`iconButton("close", t("logs.job_clear")`,
		`replaceRoute("logs", { level: state.logFilters.level, component: state.logFilters.component });`,
		`screen.__routeAttach = () => {`,
		`state.logFilters.job = cached.get("job") || "";`,
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("50-logs.js lacks the job filter step %q", want)
		}
	}
	if !strings.Contains(readUIModule(t, "00-core.js"), `logFilters: { level: "", component: "", job: "" },`) {
		t.Error("the log filter state has no job field")
	}
}
