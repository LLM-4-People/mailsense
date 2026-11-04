package ui

import (
	"io/fs"
	"strings"
	"testing"
)

// Paging models are derived from one read: dropping the read must drop the
// model at the same choke point, the way conversation pages already do. A
// per-event reset would leave the next invalidation path (live resync, a
// mutation, a reconnect) with the previous ranking or decisions on screen.
func TestInvalidationResetsDerivedPagingModels(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	invalidate := functionBody(t, js, "function invalidateCache(")
	wants := []string{
		`if (invalidated("/api/priority")) forgetPriorityPages();`,
		`if (invalidated("/api/triage")) forgetTriagePage();`,
	}
	clear := strings.Index(invalidate, "if (!prefixes.length) {")
	if clear < 0 {
		t.Fatal("invalidateCache has no full-clear branch")
	}
	for _, want := range wants {
		at := strings.Index(invalidate, want)
		if at < 0 {
			t.Fatalf("invalidateCache lacks %q", want)
		}
		if at > clear {
			t.Fatalf("%q runs after the full-clear early return", want)
		}
	}

	priority := functionBody(t, js, "function forgetPriorityPages(")
	if !strings.Contains(priority, "state.priorityPages.clear()") {
		t.Fatal("forgetPriorityPages does not clear the ranked page map")
	}
	if !strings.Contains(priority, `forgetPagingTokens("important")`) {
		t.Fatal("forgetPriorityPages leaves the cached node token behind")
	}
	triage := functionBody(t, js, "function forgetTriagePage(")
	if !strings.Contains(triage, "state.triagePage = null") {
		t.Fatal("forgetTriagePage does not clear the accumulated page")
	}
	if !strings.Contains(triage, `forgetPagingTokens("recent")`) {
		t.Fatal("forgetTriagePage leaves the cached node token behind")
	}
	tokens := functionBody(t, js, "function forgetPagingTokens(")
	if !strings.Contains(tokens, "page.screen === screen") ||
		!strings.Contains(tokens, "node.__pagingModel = null") {
		t.Fatal("forgetPagingTokens does not clear the dropped screen's cached nodes")
	}
	if !strings.Contains(tokens, `entry.node.querySelectorAll("*")`) {
		t.Fatal("forgetPagingTokens does not clear a token cached on a content node")
	}
}

// recordPriority already stores when it snapshotted a page. A cached page
// that outlived the read TTL is a cold read, not a ranking to paint while the
// screen catches up; the Home-sized rejection stays on the caller.
func TestCachedPriorityExpiresWithItsRead(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	body := functionBody(t, string(script), "function cachedPriority(")
	if !strings.Contains(body, "Date.now() - (Number(page.at) || 0) > CACHE_TTL.priority") {
		t.Fatal("cachedPriority returns a page older than its read TTL")
	}
	if !strings.Contains(body, "minItems && page.items.length < minItems") {
		t.Fatal("cachedPriority dropped the minimum-items rejection")
	}
}

// One render sequence protects one render. A cached remount keeps the same
// control and rows under a new sequence, so paging must compare the token the
// node carries, never the sequence its closure captured. The router restores
// that token with the cached node.
func TestPagingControlsUseTheirMountedToken(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	for _, signature := range []string{
		"async function loadMoreImportant(",
		"async function loadMoreAnswers(",
		"async function loadMoreTriage(",
	} {
		body := functionBody(t, js, signature)
		if strings.Contains(body, "state.renderSeq") {
			t.Errorf("%s still guards on the captured render sequence", signature)
		}
		if !strings.Contains(body, "pagingLive(") {
			t.Errorf("%s does not check the mounted paging token", signature)
		}
	}
	for _, signature := range []string{
		"function renderImportant(",
		"async function renderAnswers(",
		"async function renderRecent(",
	} {
		if body := functionBody(t, js, signature); !strings.Contains(body, "pagingToken(") {
			t.Errorf("%s does not seat a paging token on its screen node", signature)
		}
	}
	restore := functionBody(t, js, "function restoreCachedPaging(")
	for _, want := range []string{
		"readPagingToken(node)",
		"state.answersPage = page",
		"state.triagePage = page",
		"Number(page.at)",
		"CACHE_TTL.priority",
	} {
		if !strings.Contains(restore, want) {
			t.Errorf("restoreCachedPaging lacks %q", want)
		}
	}
	mount := functionBody(t, js, "async function renderRouteNow(")
	if !strings.Contains(mount, "restoreCachedPaging(route, cachedEntry.node)") {
		t.Fatal("the cached mount does not restore the paging model")
	}
	// A page names the screen that owns it, so dropping one screen's model
	// cannot clear another screen's token.
	for _, want := range []struct {
		signature string
		marker    string
	}{
		{"function renderImportant(", `screen: "important"`},
		{"async function renderAnswers(", `screen: "answers"`},
		{"async function ensureTriageView(", `screen: "recent"`},
	} {
		if body := functionBody(t, js, want.signature); !strings.Contains(body, want.marker) {
			t.Errorf("%s does not name its paging screen %q", want.signature, want.marker)
		}
	}
	// Mail caches a wrapper around the mode content; the token must travel to
	// that wrapper or every Back rebuilds Important and Recent.
	mail := functionBody(t, js, "async function renderMail(")
	if !strings.Contains(mail, "readPagingToken(content)") ||
		!strings.Contains(mail, "pagingToken(screen, contentPage)") {
		t.Fatal("renderMail does not carry the mode paging token onto the cached wrapper")
	}
}

// Load more is a busy action on all three paged screens: disabling the
// focused control must be undone with focus, and every failure still reaches
// reportError through the caller.
func TestPagingControlsUseTheSharedBusyOwner(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	for _, signature := range []string{
		"async function loadMoreImportant(",
		"async function loadMoreAnswers(",
		"async function loadMoreTriage(",
	} {
		body := functionBody(t, js, signature)
		if !strings.Contains(body, `withBusy(pagerMoreButton(pager), t("common.loading")`) &&
			!strings.Contains(body, `withBusy(page.button, t("common.loading")`) {
			t.Errorf("%s does not route its busy state through withBusy", signature)
		}
		if !strings.Contains(body, "reportActionError(error)") {
			t.Errorf("%s lost its user-action error report", signature)
		}
	}
	for _, signature := range []string{
		"async function loadMoreAnswers(",
		"async function loadMoreTriage(",
	} {
		body := functionBody(t, js, signature)
		if strings.Contains(body, "pagerMoreButton(pager).disabled") {
			t.Errorf("%s still disables its control directly", signature)
		}
	}
}
