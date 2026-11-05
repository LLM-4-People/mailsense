package ui

import (
	"strings"
	"testing"
)

// One action finishes through the topic graph, never a global wipe. The shell
// snapshots are independent reads and must start together.
func TestBackgroundActionsUseTheTopicGraph(t *testing.T) {
	actions := readUIModule(t, "51-actions.js")
	after := functionBody(t, actions, "async function afterBackgroundAction(")
	for _, want := range []string{
		"invalidateTopics(topics",
		"requires at least one invalidation topic",
		"{ refresh: false }",
		"await Promise.all([",
		"loadLearning(true)",
		"loadHub(true).catch(() => null)",
		"loadStatus(true).catch(() => null)",
		"await refreshActiveRoute()",
	} {
		if !strings.Contains(after, want) {
			t.Errorf("afterBackgroundAction lacks %q", want)
		}
	}
	for _, forbidden := range []string{"invalidateAllData(", "invalidatesCache("} {
		if strings.Contains(after, forbidden) {
			t.Errorf("afterBackgroundAction still wipes unrelated caches via %q", forbidden)
		}
	}
	if strings.Count(after, "await load") != 0 {
		t.Error("afterBackgroundAction still awaits its shell reads one by one")
	}
}

// Every action names the topics it actually changes. Sources pick/unpick,
// discovery and the manual zone confirmation are the minimum scope this
// round must pin; jobs and the preview toggle share the same owner.
func TestActionCallSitesNameTheirTopics(t *testing.T) {
	actions := readUIModule(t, "51-actions.js")
	for _, tc := range []struct{ fn, topic string }{
		{"async function discoverFromAction(", `afterBackgroundAction(["discovery"])`},
		{"async function startJobAction(", `afterBackgroundAction(["job"])`},
		{"async function unpickMailbox(", `afterBackgroundAction(["selection"], { hard: true })`},
		{"async function zoneDialog(", `afterBackgroundAction(["timeZone"])`},
		{"async function judgeDialog(", `afterBackgroundAction(["job"])`},
		{"async function syncFromAction(", `afterBackgroundAction(["job"])`},
		{"async function savePreview(", `afterBackgroundAction(["preview"], { hard: true })`},
	} {
		if body := functionBody(t, actions, tc.fn); !strings.Contains(body, tc.topic) {
			t.Errorf("%s does not use %s", tc.fn, tc.topic)
		}
	}

	sources := readUIModule(t, "25-sources.js")
	for _, tc := range []struct{ name, topic string }{
		{"the Sources discover action", `afterBackgroundAction(["discovery"])`},
		{"the Sources backfill start", `afterBackgroundAction(["job"])`},
		{"the Sources backfill cancel", `afterBackgroundAction(["job"])`},
		{"the Sources pick", `afterBackgroundAction(["selection"], { hard: true })`},
		{"the Sources undeclare", `afterBackgroundAction(["discovery", "gate"], { hard: true })`},
		{"the Sources declare", `afterBackgroundAction(["discovery", "gate"], { hard: true })`},
	} {
		if !strings.Contains(sources, tc.topic) {
			t.Errorf("%s does not use %s", tc.name, tc.topic)
		}
	}
	if strings.Contains(sources, "afterBackgroundAction();") {
		t.Error("a Sources action still calls the topic-less background path")
	}
}

// The graph entries name the reads and surfaces each action changes. A
// selection moves visible mail; discovery only the mailbox list; a job and a
// zone only the shell snapshots; a preview flips the gated reads whose
// snapshots are keyed by the preview rule: the ranked feed, the triage page,
// the Brief snapshot and the conversation states derived from that feed.
func TestActionInvalidationTopicsAreScoped(t *testing.T) {
	shell := readUIModule(t, "13-shell.js")
	discovery := topicBody(t, shell, "discovery")
	if got := jsStringArrayField(t, discovery, "reads"); !sameStrings(got, []string{
		"/api/mailboxes", "/api/status", "/api/learning", "/api/hub",
	}) {
		t.Errorf("discovery reads = %v", got)
	}
	if strings.Contains(discovery, `"/api/messages`) {
		t.Error("discovery drops the stored mail reads")
	}
	if got := jsStringArrayField(t, discovery, "routes"); !sameStrings(got, []string{"sources", "learning", "hub"}) {
		t.Errorf("discovery routes = %v", got)
	}

	selection := topicBody(t, shell, "selection")
	if !strings.Contains(selection, "reads: SELECTION_READS") {
		t.Error("selection does not use the shared selection read set")
	}
	selectionStart := strings.Index(shell, "const SELECTION_READS")
	selectionEnd := strings.Index(shell[selectionStart:], "const INVALIDATION_TOPICS")
	if selectionStart < 0 || selectionEnd <= 0 {
		t.Fatal("SELECTION_READS not found")
	}
	selectionReads := shell[selectionStart : selectionStart+selectionEnd]
	for _, want := range []string{
		"SYNC_FINISHED_READS", `"/api/search"`, `"/api/answers"`, `"/api/gate"`, `"/api/threads"`,
	} {
		if !strings.Contains(selectionReads, want) {
			t.Errorf("selection reads lack %q", want)
		}
	}
	for _, route := range []string{"sources", "mail", "companies", "answers", "labels"} {
		if !containsString(jsStringArrayField(t, selection, "routes"), route) {
			t.Errorf("selection routes lack %q", route)
		}
	}
	for _, unrelated := range []string{"settings", "logs", "ask"} {
		if containsString(jsStringArrayField(t, selection, "routes"), unrelated) {
			t.Errorf("selection stales the unrelated %s screen", unrelated)
		}
	}
	if !strings.Contains(selection, "forgetMail: true") {
		t.Error("a selection change keeps the All-Mail model")
	}

	job := topicBody(t, shell, "job")
	if got := jsStringArrayField(t, job, "reads"); !sameStrings(got, []string{
		"/api/jobs", "/api/learning", "/api/status", "/api/hub",
	}) {
		t.Errorf("job reads = %v", got)
	}
	if strings.Contains(job, `"/api/messages`) {
		t.Error("starting a job drops stored mail reads")
	}

	zone := topicBody(t, shell, "timeZone")
	if got := jsStringArrayField(t, zone, "reads"); !sameStrings(got, []string{
		"/api/status", "/api/learning", "/api/hub",
	}) {
		t.Errorf("timeZone reads = %v", got)
	}
	if !strings.Contains(zone, "allRoutes: true") {
		t.Error("a zone change no longer revalidates formatted screens")
	}
	for _, unrelated := range []string{"/api/priority", "/api/companies", "/api/messages", "/api/labels"} {
		if strings.Contains(zone, unrelated) {
			t.Errorf("a zone change drops the unrelated read %q", unrelated)
		}
	}

	preview := topicBody(t, shell, "preview")
	for _, want := range []string{
		`"/api/learning"`, `"/api/hub"`, `"/api/gate"`, `"/api/priority"`, `"/api/triage"`, `"/api/conversations"`,
		// The Brief snapshot is preview-keyed: the gate answer and the
		// ranked read behind it change with the preview rule, so the cached
		// /api/brief payload must drop with the flip.
		`"/api/brief"`,
	} {
		if !strings.Contains(preview, want) {
			t.Errorf("preview reads lack %q", want)
		}
	}
	if strings.Contains(preview, `"/api/companies/"`) {
		t.Error("toggling the preview drops unrelated company reads")
	}
}

// The reactor derives route staling from the graph: a selection stales only
// mail-derived screens, and a zone never drops a cached screen at all. This
// is the mechanical proof that unrelated route caches survive.
func TestActionTopicsDoNotClearUnrelatedRoutes(t *testing.T) {
	runUISandbox(t, "action_topics", actionTopicsHarness, "action topics: ok")
}

const actionTopicsHarness = `"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const dir = process.env.UI_DIR;
if (!dir) throw new Error("UI_DIR is required");
const app = fs.readFileSync(path.join(dir, "app.js"), "utf8");

function block(signature) {
  const start = app.indexOf(signature);
  assert.ok(start >= 0, signature + " not found");
  const end = app.indexOf("\n  }", start);
  assert.ok(end > start, signature + " has no end");
  return app.slice(start, end + 4);
}

const start = app.indexOf("// invalidation-topics:start");
const end = app.indexOf("// invalidation-topics:end");
assert.ok(start >= 0 && end > start, "the invalidation topic markers are missing");

const calls = { cache: [], stale: [], drop: [], refresh: [], forget: 0 };
const sandbox = {
  state: { questions: null },
  invalidateCache: (prefixes) => calls.cache.push(prefixes),
  staleRouteCache: (...names) => calls.stale.push(names),
  invalidateRouteCache: (...names) => calls.drop.push(names),
  scheduleDataRefresh: (...names) => calls.refresh.push(names),
  forgetAllMail: () => { calls.forget += 1; },
};
vm.createContext(sandbox);
const script = app.slice(start, end) + "\n" + block("function invalidateTopics(") + "\nINVALIDATION_TOPICS;";
vm.runInContext(script, sandbox, { filename: "app.js#topics" });

function reset() {
  calls.cache = [];
  calls.stale = [];
  calls.drop = [];
  calls.refresh = [];
  calls.forget = 0;
}
function sorted(values) {
  return Array.from(values).sort();
}

reset();
sandbox.invalidateTopics(["selection"], { hard: true });
assert.equal(calls.drop.length, 1, "a selection did not drop its screens");
assert.ok(calls.drop[0].includes("mail"), "a selection did not drop Mail");
assert.ok(calls.drop[0].includes("sources"), "a selection did not drop Sources");
for (const unrelated of ["settings", "logs", "ask"]) {
  assert.ok(!calls.drop[0].includes(unrelated), "a selection dropped " + unrelated);
}
assert.ok(calls.cache[0].includes("/api/messages?"), "a selection lost the message list read");
assert.ok(calls.cache[0].includes("/api/search"), "a selection lost the search read");
assert.ok(calls.cache[0].includes("/api/gate"), "a selection lost the gate read");
assert.equal(calls.forget, 1, "a selection did not drop the All-Mail model");

reset();
sandbox.invalidateTopics(["discovery"]);
assert.equal(calls.drop.length, 0, "discovery dropped a cached screen");
assert.equal(calls.stale.length, 1, "discovery did not stale its screens");
assert.deepEqual(sorted(calls.stale[0]), sorted(["sources", "learning", "hub"]));
assert.ok(!calls.cache[0].includes("/api/messages?"), "discovery dropped the message read");
assert.ok(!calls.cache[0].includes("/api/search"), "discovery dropped search");

reset();
sandbox.invalidateTopics(["timeZone"]);
assert.equal(calls.drop.length, 0, "a zone change dropped cached screens");
assert.equal(calls.stale.length, 1, "a zone change did not stale the formatted screens");
assert.equal(calls.stale[0].length, 0, "a zone change staled only some screens");
assert.deepEqual(sorted(calls.cache[0]), sorted(["/api/status", "/api/learning", "/api/hub"]));
assert.equal(calls.forget, 0, "a zone change dropped the All-Mail model");

reset();
sandbox.invalidateTopics(["job"]);
assert.equal(calls.drop.length, 0, "starting a job dropped a cached screen");
assert.deepEqual(sorted(calls.stale[0]), sorted(["sources", "learning", "hub"]));

reset();
sandbox.invalidateTopics(["preview"], { hard: true });
assert.equal(calls.drop.length, 1, "a preview toggle did not drop the gated screens");
assert.ok(calls.drop[0].includes("hub"), "a preview toggle kept Home");
assert.ok(calls.drop[0].includes("mail"), "a preview toggle kept Mail");
assert.ok(!calls.drop[0].includes("settings"), "a preview toggle dropped Settings");
assert.ok(calls.cache[0].includes("/api/gate"), "a preview toggle lost the gate read");
assert.ok(calls.cache[0].includes("/api/priority"), "a preview toggle kept the ranked snapshot");
assert.ok(calls.cache[0].includes("/api/triage"), "a preview toggle kept the triage page");
assert.ok(calls.cache[0].includes("/api/conversations"), "a preview toggle kept the pre-flip conversation states");
assert.ok(calls.cache[0].includes("/api/brief"), "a preview toggle kept the pre-flip Brief snapshot");

console.log("action topics: ok");
`
