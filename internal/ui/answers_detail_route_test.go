package ui

import (
	"strings"
	"testing"
)

// An Answers row click changes the detail pane, not the screen identity.
// The message id must stay out of the route cache key so the loaded pages,
// the list scroll and the focused row survive opening and closing it.
func TestAnswersRouteKeyKeepsTheLoadedPages(t *testing.T) {
	runUISandbox(t, "answers_route_key", answersRouteKeyHarness, "answers route key: ok")

	router := readUIModule(t, "12-router.js")
	key := functionBody(t, router, "function routeCacheKey(")
	if !strings.Contains(key, `if (route.name === "answers") params.delete("message");`) {
		t.Fatal("routeCacheKey keeps the Answers message id in the screen identity")
	}
	for _, call := range []string{
		"syncCachedAnswersDetail(route)",
		"syncCachedAnswersDetail(route);",
	} {
		if !strings.Contains(router, call) {
			t.Errorf("the router never applies a cached Answers detail change (%q)", call)
		}
	}
	if !strings.Contains(router, `const host = document.querySelector(".answers-screen .answers-detail-host");`) {
		t.Fatal("syncCachedAnswersDetail does not own the mounted detail host")
	}
	sync := functionBody(t, router, "function syncCachedAnswersDetail(")
	if !strings.Contains(sync, "{ reveal: false }") {
		t.Fatal("a row click still scrolls the list to the highlighted answer group")
	}
}

// The detail host is permanent so the router can patch it without rebuilding
// the list. A closed pane restores the list offset it pushed away from.
func TestAnswersDetailPatchesInPlace(t *testing.T) {
	answers := readUIModule(t, "27-answers.js")
	for _, want := range []string{
		`const detailHost = h("div", { class: "answers-detail-host", hidden: true });`,
		`syncAnswersDetail(detailHost, messageId, detailQuestion, seq);`,
		`function syncAnswersDetail(host, messageId, requestedQuestion, seq, options = {})`,
		`restoreAnswersListScroll(host);`,
		`function syncAnswersListSelection(hash)`,
		`state.answerDetailSeq += 1;`,
	} {
		if !strings.Contains(answers, want) {
			t.Errorf("27-answers.js lacks %q", want)
		}
	}
	sync := functionBody(t, answers, "function syncAnswersDetail(")
	if strings.Contains(sync, "innerHTML") {
		t.Fatal("the detail patch builds markup through innerHTML")
	}
	for _, forbidden := range []string{"renderAnswers(", "invalidateCache(", "loadMoreAnswers("} {
		if strings.Contains(sync, forbidden) {
			t.Errorf("the detail patch rebuilds the list via %q", forbidden)
		}
	}
	// A deep link still reveals its highlighted group; only the in-place row
	// open suppresses the jump.
	detail := functionBody(t, answers, "async function loadAnswerDetail(")
	if !strings.Contains(detail, "options.reveal !== false") {
		t.Fatal("loadAnswerDetail cannot distinguish a deep link from a row click")
	}
}

const answersRouteKeyHarness = `"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const dir = process.env.UI_DIR;
if (!dir) throw new Error("UI_DIR is required");
const app = fs.readFileSync(path.join(dir, "app.js"), "utf8");
const start = app.indexOf("function routeCacheKey(");
assert.ok(start >= 0, "routeCacheKey not found");
const end = app.indexOf("\n  }", start);
assert.ok(end > start, "routeCacheKey has no end");
const script = app.slice(start, end + 4) +
  "\nrouteCacheKey;";
const scope = { URLSearchParams };
vm.createContext(scope);
const routeCacheKey = vm.runInContext(script, scope, { filename: "app.js#route-key" });

function route(name, params) {
  return { name, params: new URLSearchParams(params || {}) };
}

assert.equal(
  routeCacheKey(route("answers", { question: "category", message: "12" })),
  routeCacheKey(route("answers", { question: "category" })),
  "the Answers message id still creates a new screen identity",
);
assert.equal(
  routeCacheKey(route("answers", { question: "category", source: "manual", message: "12" })),
  routeCacheKey(route("answers", { question: "category", source: "manual" })),
  "the Answers message id still creates a new filtered identity",
);
assert.notEqual(
  routeCacheKey(route("answers", { question: "category", message: "12" })),
  routeCacheKey(route("answers", { question: "other", message: "12" })),
  "the question filter no longer changes the screen identity",
);
assert.equal(
  routeCacheKey(route("mail", { mode: "important", id: "7" })),
  routeCacheKey(route("mail", { mode: "important" })),
  "the Mail detail id is back in the screen identity",
);
assert.equal(
  routeCacheKey(route("companies", { key: "c:acme" })),
  routeCacheKey(route("companies", {})),
  "the company key is back in the screen identity",
);
assert.notEqual(
  routeCacheKey(route("hub", {})),
  routeCacheKey(route("mail", {})),
  "two different screens share one route key",
);

console.log("answers route key: ok");
`
