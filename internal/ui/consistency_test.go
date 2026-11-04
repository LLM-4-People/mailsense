package ui

import (
	"strings"
	"testing"
)

// A confirmation awaits, so the click target is gone by the time the clear
// work starts. Capture it before the dialog or the busy state can never
// attach to the button.
func TestAskHistoryClearCapturesItsTrigger(t *testing.T) {
	body := functionBody(t, readUIModule(t, "23-ask-history.js"), "async function clearAskHistory(")
	for _, want := range []string{
		"const trigger = event && event.currentTarget ? event.currentTarget : null;",
		"withBusy(trigger, null",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("clearAskHistory lacks %q", want)
		}
	}
	if strings.Contains(body, "withBusy(event.currentTarget") {
		t.Fatal("clearAskHistory reads currentTarget after the dialog closed")
	}
}

// The five paged screens share one footer primitive. The old per-screen
// classes must not come back beside it.
func TestSharedPagerOwnsEveryPagingFooter(t *testing.T) {
	for _, tc := range []struct{ file, call string }{
		{"32-mail.js", "const more = pagerNode("},
		{"32-mail.js", "const foot = pagerNode("},
		{"32-important.js", "pagerPaint(page.pager"},
		{"32-recent.js", "pagerPaint(pager"},
		{"27-answers.js", "const pager = pagerNode("},
	} {
		if !strings.Contains(readUIModule(t, tc.file), tc.call) {
			t.Errorf("%s does not use the shared pager (%q)", tc.file, tc.call)
		}
	}
	for _, file := range []string{"32-mail.js", "32-important.js", "32-recent.js", "27-answers.js"} {
		source := readUIModule(t, file)
		for _, stale := range []string{"mail-more", "answers-foot", "conversation-foot"} {
			if strings.Contains(source, stale) {
				t.Errorf("%s still names the removed footer %q", file, stale)
			}
		}
	}
}

// The Mail detail and the triage list render stored answers through the one
// value formatter; only the uncertainty wording belongs to the detail.
func TestMailDetailUsesTheSharedAnswerValueText(t *testing.T) {
	source := readUIModule(t, "33-search.js")
	for _, want := range []string{
		"return answerValueText(question, reading.choice)",
		"let label = answerValueText(question, reading.level);",
		"if (reading.uncertain) label += t(\"detail.summary.uncertain_suffix\");",
	} {
		if !strings.Contains(source, want) {
			t.Errorf("33-search.js lacks %q", want)
		}
	}
	if strings.Contains(source, "function triageChoiceOptionLabel(") {
		t.Fatal("the duplicate choice-label wrapper survived")
	}
}

// A failed priority read must stay visible when the agenda supplies the
// fallback rows.
func TestHubBriefShowsBothTheFallbackAndTheFailedRead(t *testing.T) {
	body := functionBody(t, readUIModule(t, "20-hub.js"), "function homeBriefSignals(")
	if !strings.Contains(body, "if (!priorityError) return rows;") ||
		!strings.Contains(body, "hub.brief.signals_unavailable") {
		t.Fatal("homeBriefSignals lets the agenda fallback hide a failed priority read")
	}
}

// A capped search total is a lower bound. Both the count sentence and the
// notice must say so, independent of the narrower text-match cap.
func TestCappedSearchTotalIsALowerBound(t *testing.T) {
	source := readUIModule(t, "33-search.js")
	for _, want := range []string{
		"fmtCount(hits.length, Number(result.total) || 0, !!result.capped)",
		"if (capped) return t(\"common.result_count_at_least\", { count: total });",
		"result.capped ? h(\"p\", { text: t(\"search.capped_total\") }) : null,",
	} {
		if !strings.Contains(source, want) {
			t.Errorf("33-search.js lacks %q", want)
		}
	}
}

// The agenda truncation notice has one owner and both renderers call it.
func TestAgendaTruncationNoticeIsShared(t *testing.T) {
	hub := readUIModule(t, "20-hub.js")
	if !strings.Contains(hub, "function agendaTruncationNotice(view)") ||
		!strings.Contains(hub, "agendaTruncationNotice(view))") {
		t.Fatal("the Home agenda does not render the truncation notice")
	}
	ask := readUIModule(t, "22-ask.js")
	if !strings.Contains(ask, "agendaTruncationNotice(res)") {
		t.Fatal("the Ask agenda does not render the truncation notice")
	}
}

// The Recent page order is the server's order, not the map's key order. The
// ids helpers are pure and shared by the first page and load more.
func TestTriageOrderFollowsServerIds(t *testing.T) {
	mail := readUIModule(t, "32-mail.js")
	if strings.Contains(mail, "Object.keys(view.results).map(Number).sort((a, b) => b - a)") {
		t.Fatal("32-mail.js still re-sorts the triage ids by row id")
	}
	for _, want := range []string{
		"const pageIds = triagePageIds(page, results);",
		"view.ids = firstPage ? pageIds : pageIds.concat(view.ids.filter((id) => !pageIds.includes(id)));",
		`const selectedId = triageSelectedId(visibleIds, Number(route.params.get("id")) || 0);`,
	} {
		if !strings.Contains(mail, want) {
			t.Errorf("32-mail.js lacks %q", want)
		}
	}
	recent := readUIModule(t, "32-recent.js")
	for _, want := range []string{
		"function triagePageIds(",
		"const added = triageAppendIds(view.ids, pageIds);",
		"function triageSelectedId(",
	} {
		if !strings.Contains(recent, want) {
			t.Errorf("32-recent.js lacks %q", want)
		}
	}
	runUISandbox(t, "triage_order_test", triageOrderHarness, "triage order: ok")
}

const triageOrderHarness = `"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const dir = process.env.UI_DIR;
if (!dir) throw new Error("UI_DIR is required");

const sandbox = {};
sandbox.window = sandbox;
vm.createContext(sandbox);
const app = fs.readFileSync(path.join(dir, "app.js"), "utf8");
const start = app.indexOf("function triagePageIds(");
const end = app.indexOf("async function loadMoreTriage(", start);
assert.ok(start >= 0 && end > start, "the triage order helpers are missing");
vm.runInContext(app.slice(start, end), sandbox, { filename: "app.js#triage-order" });

const rows = (...ids) => Object.fromEntries(ids.map((id) => [id, { message_id: id }]));

// Explicit ids win over the object's key order, duplicates keep their first
// position, and an id without a row is ignored.
assert.deepEqual(Array.from(sandbox.triagePageIds({ ids: [10, 9] }, rows(9, 10))), [10, 9]);
assert.deepEqual(Array.from(sandbox.triagePageIds({ ids: [2, 1] }, rows(2, 1))), [2, 1]);
assert.deepEqual(Array.from(sandbox.triagePageIds({ ids: [2, 2, 1] }, rows(1, 2))), [2, 1]);
assert.deepEqual(Array.from(sandbox.triagePageIds({ ids: [10, 4] }, rows(4))), [4]);

// An older server without ids keeps the descending row-id fallback.
assert.deepEqual(Array.from(sandbox.triagePageIds({}, rows(9, 10))), [10, 9]);

const target = [10, 9];
assert.deepEqual(Array.from(sandbox.triageAppendIds(target, [9, 8, 7])), [8, 7]);
assert.deepEqual(target, [10, 9, 8, 7]);

assert.equal(sandbox.triageSelectedId([10, 9], 10), 10);
assert.equal(sandbox.triageSelectedId([10, 9], 99), 10);
assert.equal(sandbox.triageSelectedId([], 99), 0);
assert.deepEqual(Array.from(sandbox.triageVisibleIds({
  ids: [10, 9],
  results: { 10: { escalate: false }, 9: { escalate: true } },
}, true)), [9]);

console.log("triage order: ok");
`

// The sender filter chip is one component. The mail list and the search
// screen each spelled its markup, its two i18n keys and its clear control, so
// a change to the chip (a new attribute, a different aria label) could reach
// one screen and miss the other. The component owns the markup; the screens
// pass only the sender and what clearing it means for their own filter state.
func TestSenderChipIsOneComponent(t *testing.T) {
	components := readUIModule(t, "01-components.js")
	body := functionBody(t, components, "function senderChip(")
	for _, want := range []string{
		`class: "chip sender-chip"`,
		`class: "sender-chip-label"`,
		`t("mail.filter.sender_chip", { sender })`,
		`t("mail.filter.sender_clear")`,
		`onclick: onClear`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("senderChip lacks %q", want)
		}
	}
	if !strings.Contains(body, "if (!sender) return null;") {
		t.Error("senderChip must render nothing without a sender, so its call sites need no ternary")
	}

	for _, tc := range []struct{ file, call string }{
		{"32-mail.js", "senderChip(sender, clearSender)"},
		{"33-search.js", "senderChip(sender, clearSender)"},
	} {
		module := readUIModule(t, tc.file)
		if !strings.Contains(module, tc.call) {
			t.Errorf("%s does not render the shared sender chip (%q)", tc.file, tc.call)
		}
		if strings.Contains(module, `"chip sender-chip"`) {
			t.Errorf("%s still builds the sender chip markup by hand", tc.file)
		}
	}
}

// The shell search bar is the mounted screen's own filter, so the router has
// one rule for where a mount leaves the keyboard and one gate for whether a
// background repaint may replace the screen. Both must treat a focused shell
// bar like a focused control inside the screen: the mount leaves the caret
// alone and the repaint waits. The router used to send focus to #main after
// every mount and ignored the bar in canReplaceMain, so typing a scoped query
// kept its first character and a repaint could swap the screen under the caret.
func TestNavigationFocusKeepsTheShellSearchBar(t *testing.T) {
	router := readUIModule(t, "12-router.js")
	focus := functionBody(t, router, "function restoreNavigationFocus(")
	for _, want := range []string{
		"if (state.askFocusPending) {",
		"if (active === dom.globalSearchInput) return;",
		"if (!dom.main.contains(active)) dom.main.focus({ preventScroll: true });",
	} {
		if !strings.Contains(focus, want) {
			t.Errorf("restoreNavigationFocus lacks %q", want)
		}
	}
	replace := functionBody(t, router, "function canReplaceMain(")
	if !strings.Contains(replace, "if (active && active === dom.globalSearchInput) return false;") {
		t.Error("canReplaceMain replaces the screen while the owner types in the shell search bar")
	}

	// Both mount paths ask the one rule. restoreMainFocus keeps its own #main
	// fallback for a repaint that destroyed the focused row; the mounts must
	// not add a third policy.
	if got := strings.Count(router, "restoreNavigationFocus();"); got != 2 {
		t.Errorf("the mount paths call restoreNavigationFocus %d times, want 2", got)
	}
	if got := strings.Count(router, "state.askFocusPending ?"); got != 0 {
		t.Errorf("%d inline ask-focus ternaries; the mounts must ask restoreNavigationFocus", got)
	}
}

// A slow mount can land between two keystrokes, so the route it paints may
// still carry the previous query. While the shell bar holds focus its text is
// newer than the route: the screen adopts it instead of erasing the last
// character, and filters with it. The old rule only adopted the typed text
// when the route had none, so a loaded machine silently dropped keystrokes.
func TestScopedSearchKeepsNewerTypedText(t *testing.T) {
	body := functionBody(t, readUIModule(t, "33-search.js"), "function activateScopedSearch(")
	for _, want := range []string{
		`const typed = document.activeElement === dom.globalSearchInput ? dom.globalSearchInput.value : "";`,
		"if (typed && typed !== value) {",
		"value = typed;",
		"scoped.apply(value);",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("activateScopedSearch lacks %q", want)
		}
	}
}

// The shell's route attribute is one fact with one writer. The language pass
// re-spelled it from state.route, which could name a route the router had not
// mounted (and, with no token, one it never would). The router's setHeader is
// the owner; every other module reads the attribute through expectRoute.
func TestRouteAttributeHasOneWriter(t *testing.T) {
	names, err := moduleNames("js", ".js")
	if err != nil {
		t.Fatal(err)
	}
	writers := []string{}
	for _, name := range names {
		if strings.Contains(readUIModule(t, name), "dataset.screen =") {
			writers = append(writers, name)
		}
	}
	if len(writers) != 1 || writers[0] != "12-router.js" {
		t.Fatalf("route attribute writers %v; setHeader in 12-router.js is the only one", writers)
	}
	body := functionBody(t, readUIModule(t, "12-router.js"), "function setHeader(")
	if !strings.Contains(body, "document.documentElement.dataset.screen = name;") {
		t.Fatal("setHeader no longer writes the route attribute")
	}
}
