package ui

import (
	"io/fs"
	"strings"
	"testing"
)

// blockBody returns one source block from its signature to the given end
// marker. The UI source is a flat script, so the marker is the owner's close.
func blockBody(t *testing.T, js, signature, end string) string {
	t.Helper()
	start := strings.Index(js, signature)
	if start < 0 {
		t.Fatalf("%q not found", signature)
	}
	stop := strings.Index(js[start:], end)
	if stop <= 0 {
		t.Fatalf("the end of %q was not found", signature)
	}
	return js[start : start+stop]
}

func functionBody(t *testing.T, js, signature string) string {
	t.Helper()
	return blockBody(t, js, signature, "\n  }")
}

func handlerBody(t *testing.T, js, signature string) string {
	t.Helper()
	return blockBody(t, js, signature, "\n    });")
}

// A background data event advances what a screen reads; it must not throw
// away a screen the owner left. Its mounted DOM keeps its scroll and focus,
// and the route revalidates after the next mount. A local write or a language
// change still drops the DOM because the old markup itself is wrong.
func TestBackgroundDataEventsKeepMountedScreens(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)

	stale := functionBody(t, js, "function staleRouteCache(")
	if !strings.Contains(stale, "entry.stale = true") {
		t.Fatal("staleRouteCache does not mark the entry")
	}
	if strings.Contains(stale, "routeCache.delete") {
		t.Fatal("staleRouteCache still deletes the mounted screen")
	}

	mount := functionBody(t, js, "async function renderRouteNow(")
	if !strings.Contains(mount, "cachedEntry.stale") || !strings.Contains(mount, "refreshCachedRoute(") {
		t.Fatal("a stale cached route is not revalidated after mounting")
	}

	refresh := functionBody(t, js, "function refreshCachedRoute(")
	if !strings.Contains(refresh, "entry.stale = false") {
		t.Fatal("a completed revalidation does not clear the stale flag")
	}

	for _, handler := range []string{
		`source.addEventListener("index.finished"`,
		`source.addEventListener("sync.mailboxes"`,
		`source.addEventListener("config.changed"`,
	} {
		body := handlerBody(t, js, handler)
		if !strings.Contains(body, "invalidateTopics(") {
			t.Errorf("%s does not go through the topic graph", handler)
		}
		if strings.Contains(body, "invalidateRouteCache(") {
			t.Errorf("%s drops the mounted screens", handler)
		}
	}
	changed := handlerBody(t, js, `source.addEventListener("config.changed"`)
	if strings.Contains(changed, "invalidateAllData()") {
		t.Fatal("a remote configuration change still drops every cached screen")
	}

	after := functionBody(t, js, "function afterLearningDataChanged(")
	if !strings.Contains(after, `invalidateTopics(["learning"])`) || strings.Contains(after, "invalidateRouteCache(") {
		t.Fatal("a finished learning stage drops the mounted screens")
	}

	// The hard paths keep their drop: a local write and a language switch
	// replace the markup, not just its data.
	if body := functionBody(t, js, "function invalidateLearningDerived("); !strings.Contains(body, `invalidateTopics(["learning"], { hard: true })`) {
		t.Fatal("the write path no longer drops the derived routes")
	}
	if body := functionBody(t, js, "function invalidateAllData("); !strings.Contains(body, "invalidateRouteCache()") {
		t.Fatal("the broad write path no longer drops the mounted screens")
	}
}

// A Mail tab is cached under the routed name "mail"; a topic that names the
// surface it changed (search, priority, triage) must stale or drop that tab.
func TestRouteCacheMatchesMailSurfaces(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	entry := functionBody(t, js, "function routeCacheEntry(")
	if !strings.Contains(entry, `surface: route.name === "mail" ? mailSurface(route) : ""`) {
		t.Fatal("a cached Mail tab does not carry its surface")
	}
	matches := functionBody(t, js, "function routeCacheMatches(")
	if !strings.Contains(matches, "entry.name === name") || !strings.Contains(matches, "entry.surface === name") {
		t.Fatal("route-cache matching ignores the Mail surface")
	}
	stale := functionBody(t, js, "function staleRouteCache(")
	if !strings.Contains(stale, "routeCacheMatches(entry, name)") {
		t.Fatal("staleRouteCache does not match a Mail surface")
	}
	drop := functionBody(t, js, "function invalidateRouteCache(")
	if !strings.Contains(drop, "routeCacheMatches(entry, name)") {
		t.Fatal("invalidateRouteCache does not match a Mail surface")
	}
}

// A live language switch changes every label built with t(), but the route
// identity still matches the mounted route. The language path must force a
// full body rebuild instead of taking the same-route early return.
func TestLanguageSwitchForcesMountedBodyRebuild(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)

	language := functionBody(t, js, "async function applyLanguageLive(")
	if !strings.Contains(language, "invalidateRouteCache()") {
		t.Fatal("the language path no longer drops cached route nodes")
	}
	if !strings.Contains(language, "renderRoute(true)") {
		t.Fatal("the language path does not force the mounted body to rebuild")
	}

	route := functionBody(t, js, "async function renderRoute(")
	if !strings.Contains(route, "renderRouteNow(force)") {
		t.Fatal("renderRoute does not forward the force flag")
	}
	mount := functionBody(t, js, "async function renderRouteNow(")
	if !strings.Contains(mount, "async function renderRouteNow(force = false)") {
		t.Fatal("renderRouteNow lost its force parameter")
	}
	if !strings.Contains(mount, "const sameRoute = !force && state.routeKey === key") {
		t.Fatal("the same-route branch ignores the force flag")
	}
	if !strings.Contains(mount, "const cachedEntry = force ? null : routeCache.get(key)") {
		t.Fatal("a forced rebuild can still paint the cached node")
	}
}

// A cached node is markup only: leaving the route disconnects its keyboard
// handler and observers. Each screen declares one attach hook on its screen
// node, and the cached restore must invoke it (Companies and Teach).
func TestCachedRestoreRunsScreenAttachHook(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)

	entry := functionBody(t, js, "function routeCacheEntry(")
	if !strings.Contains(entry, "attach: typeof node.__routeAttach") {
		t.Fatal("route cache entries do not capture the screen attach hook")
	}
	attach := functionBody(t, js, "function attachCachedRoute(")
	if !strings.Contains(attach, "entry.attach()") {
		t.Fatal("the route cache does not invoke the attach hook")
	}
	mount := functionBody(t, js, "async function renderRouteNow(")
	if !strings.Contains(mount, "attachCachedRoute(key)") {
		t.Fatal("a cached mount does not reattach its screen")
	}

	labels := functionBody(t, js, "async function renderLabels(")
	if !strings.Contains(labels, "screen.__routeAttach") {
		t.Fatal("Teach does not declare an attach hook")
	}
	if !strings.Contains(labels, "setLabelKeyHandler(handleKey)") {
		t.Fatal("the Teach attach hook does not restore its keyboard handler")
	}
	if !strings.Contains(labels, "new ResizeObserver(syncActionHeight)") ||
		!strings.Contains(labels, "observeLabelChips(") {
		t.Fatal("the Teach attach hook does not recreate both observers")
	}
	companies := functionBody(t, js, "async function renderCompanies(")
	if !strings.Contains(companies, "screen.__routeAttach = reattachCompanyRows") {
		t.Fatal("Companies no longer declares its existing reattach hook")
	}
}

// The client request has one timeout owner. A future rewrite cannot drop the
// abort, the timer, or the one mapping to ApiError(0, {code:"timeout"}).
func TestRequestOwnsTimeout(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)

	for _, constant := range []string{
		"const REQUEST_TIMEOUT_MS = 30000;",
		"const LONG_REQUEST_TIMEOUT_MS = 300000;",
	} {
		if !strings.Contains(js, constant) {
			t.Errorf("the HTTP owner lost %q", constant)
		}
	}
	request := functionBody(t, js, "async function request(")
	for _, required := range []string{
		"new AbortController()",
		"window.setTimeout(() => controller.abort(), timeoutMs)",
		"signal: controller.signal",
		"window.clearTimeout(timer)",
		`new ApiError(0, { code: "timeout" })`,
		`cache: options.cache || "no-store"`,
		"error.mutation = mutation",
		"if (mutation) recoverUnknownMutation()",
	} {
		if !strings.Contains(request, required) {
			t.Errorf("request() lost %q", required)
		}
	}
	if strings.Contains(request, "EventSource") {
		t.Fatal("the streaming path uses the bounded request() client")
	}
	// Attachments are the only read allowed to use the browser cache, and the
	// only one that asks for a blob; every other request keeps no-store JSON.
	body := readUIModule(t, "30-mail-body.js")
	if !strings.Contains(body, `{ as: "blob", cache: "default" }`) {
		t.Error("the attachment read does not use request with a blob and the cache")
	}
	if strings.Contains(body, "fetch(") {
		t.Error("the attachment read bypasses request()")
	}
	ask := functionBody(t, js, "function createAskWorkspace(")
	if !strings.Contains(ask, `request("/api/ask", { method: "POST", body: payload, timeoutMs: LONG_REQUEST_TIMEOUT_MS })`) {
		t.Fatal("Ask send does not use the long request bound")
	}
	start := functionBody(t, js, "async function startJobAction(")
	if !strings.Contains(start, "timeoutMs: LONG_REQUEST_TIMEOUT_MS") {
		t.Fatal("a job start does not use the long request bound")
	}
}

// The top-level action runner reports a rejected action exactly once by
// default; a screen that already shows the failure inline opts out.
func TestTopLevelActionsReportByDefault(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)

	run := functionBody(t, js, "async function runAction(")
	if !strings.Contains(run, "withBusy(trigger, label, fn)") {
		t.Fatal("runAction no longer restores its trigger through withBusy")
	}
	if !strings.Contains(run, "options.report !== false") || !strings.Contains(run, "reportActionError(error)") {
		t.Fatal("runAction does not report a rejected action by default")
	}
	sources := functionBody(t, js, "async function renderSources(")
	if !strings.Contains(sources, `runAction(event.currentTarget, t("sources.refreshing")`) {
		t.Fatal("Sources Refresh still swallows its failed read")
	}
	logs := functionBody(t, js, "async function renderLogs(")
	if !strings.Contains(logs, `runAction(event.currentTarget, t("sources.refreshing")`) {
		t.Fatal("Logs Refresh still swallows its failed refresh")
	}
}
