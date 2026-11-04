package ui

import (
	"strings"
	"testing"
)

// A failed /api/gate refresh is permission data, not a screen read. Sources,
// Labels, Ask and Home go through readGate, bind its settled result, and keep
// painting the reads that succeeded instead of rejecting the whole route.
func TestGateFailureCannotRejectScreenReads(t *testing.T) {
	shell := readUIModule(t, "13-shell.js")
	for _, want := range []string{
		"function readGate()",
		"return settleRead(loadGate());",
	} {
		if !strings.Contains(shell, want) {
			t.Errorf("13-shell.js lacks the non-fatal gate read %q", want)
		}
	}
	for _, tc := range []struct{ file, render, binding, own string }{
		{"20-hub.js", "async function renderHub(", "const [view, gateRead]", "loadHub()"},
		{"22-ask.js", "async function renderAsk(", "const [gateRead, provider", "readAnswerProvider()"},
		{"25-sources.js", "async function renderSources(", "const [status, mailboxes, jobsRead, gateRead, heldRead]", "loadStatus()"},
		{"26-labels.js", "async function renderLabels(", "const [questionsView, learning, summaryRead, gateRead]", "loadQuestions()"},
	} {
		body := functionBody(t, readUIModule(t, tc.file), tc.render)
		if !strings.Contains(body, tc.binding) {
			t.Errorf("%s does not bind the gate result in its settled read slot (%q)", tc.file, tc.binding)
		}
		if strings.Contains(body, "loadGate()") {
			t.Errorf("%s still awaits the gate directly; use readGate()", tc.file)
		}
		if !strings.Contains(body, "readGate()") {
			t.Errorf("%s does not refresh the gate through readGate()", tc.file)
		}
		if !strings.Contains(body, tc.own) {
			t.Errorf("%s no longer starts its own read %q", tc.file, tc.own)
		}
		if !strings.Contains(body, "gateRead") {
			t.Errorf("%s ignores the settled gate result", tc.file)
		}
		if !strings.Contains(body, "gateRead.value") && !strings.Contains(body, "gateRead.error") {
			t.Errorf("%s binds the gate result without consuming it", tc.file)
		}
	}
}

// Permission has one owner: the shell's gate snapshot. Learning never reads
// its own view's open flag, and Home never reads the hub content's open flag;
// hubGateContent is the only reader of the content object.
func TestScreenPermissionComesOnlyFromTheGateSnapshot(t *testing.T) {
	learning := readUIModule(t, "23-learning.js")
	for _, banned := range []string{"view.open", "view.preview", "view.gate"} {
		if strings.Contains(learning, banned) {
			t.Errorf("23-learning.js still derives permission via %q", banned)
		}
	}
	for _, want := range []string{
		"const open = permissionKnown && gateOpen()",
		"const preview = permissionKnown && gatePreview()",
	} {
		if !strings.Contains(learning, want) {
			t.Errorf("23-learning.js lacks the gate owner call %q", want)
		}
	}

	hub := readUIModule(t, "20-hub.js")
	for _, banned := range []string{"gate.open", "!!view.open"} {
		if strings.Contains(hub, banned) {
			t.Errorf("20-hub.js still reads permission via %q", banned)
		}
	}
	for _, want := range []string{
		"function hubGateContent(view)",
		"gateKnown()",
		"gateOpen()",
		"gatePreview()",
		"readGate()",
	} {
		if !strings.Contains(hub, want) {
			t.Errorf("20-hub.js lacks the gate owner call %q", want)
		}
	}
	content := functionBody(t, hub, "function hubGateContent(view) {")
	if !strings.Contains(content, "view.gate") {
		t.Error("hubGateContent does not read the hub gate content")
	}
	if got, want := strings.Count(hub, "view.gate"), strings.Count(content, "view.gate"); got != want {
		t.Errorf("20-hub.js reads the hub content object %d time(s), want hubGateContent to own all %d", got, want)
	}

	// The skeleton topbar shares the same rule: an unknown gate is not painted
	// as a closed one while the first screen mounts.
	quick := functionBody(t, readUIModule(t, "12-router.js"), "function quickRouteMeta(route) {")
	for _, want := range []string{
		"const known = gateKnown();",
		"known && gateOpen()",
		"known && gatePreview()",
		"known ? gatePill(open) : null",
	} {
		if !strings.Contains(quick, want) {
			t.Errorf("quickRouteMeta lacks the gate owner guard %q", want)
		}
	}
}

// state.jobs and loadJobs are the one job model, and the label summary is one
// boundary fact: the current hub snapshot or the dedicated endpoint, never
// both in one render.
func TestJobsAndLabelSummaryHaveOneOwner(t *testing.T) {
	jobs := readUIModule(t, "13-jobs.js")
	for _, want := range []string{
		"function jobSnapshot()",
		"return Array.from(state.jobs.values());",
		"async function loadJobs(force = false)",
		"state.jobs.delete(id)",
	} {
		if !strings.Contains(jobs, want) {
			t.Errorf("13-jobs.js lacks the owner %q", want)
		}
	}
	shell := readUIModule(t, "13-shell.js")
	for _, want := range []string{
		"function labelSummarySnapshot(allowStale = false)",
		`if (!allowStale && cached("/api/hub") === undefined) return null;`,
		"function readLabelSummary()",
		"function labelReviewCount()",
	} {
		if !strings.Contains(shell, want) {
			t.Errorf("13-shell.js lacks the owner %q", want)
		}
	}
	for _, tc := range []struct{ file, forbidden string }{
		{"23-learning.js", `requestCached("/api/jobs"`},
		{"23-learning.js", `requestCached("/api/labels/summary"`},
		{"25-sources.js", `requestCached("/api/jobs"`},
		{"26-labels.js", `requestCached("/api/labels/summary"`},
	} {
		if strings.Contains(readUIModule(t, tc.file), tc.forbidden) {
			t.Errorf("%s keeps a second read owner (%q)", tc.file, tc.forbidden)
		}
	}
	if !strings.Contains(readUIModule(t, "23-learning.js"), "jobSnapshot()") {
		t.Error("Learning does not paint the shared job model")
	}
	if !strings.Contains(readUIModule(t, "25-sources.js"), "settleRead(loadJobs())") {
		t.Error("Sources does not read the shared job snapshot through loadJobs")
	}
	for _, file := range []string{"23-learning.js", "26-labels.js"} {
		js := readUIModule(t, file)
		if !strings.Contains(js, "readLabelSummary()") {
			t.Errorf("%s does not use the one label-summary read", file)
		}
		if strings.Contains(js, "state.hub") {
			t.Errorf("%s reads the hub snapshot directly instead of the label-summary owner", file)
		}
	}
	prefetch := functionBody(t, shell, "function prefetchCore()")
	if !strings.Contains(prefetch, "loadHub()") {
		t.Error("the core prefetch no longer seeds the hub snapshot")
	}
	if strings.Contains(prefetch, `"/api/labels/summary"`) {
		t.Error("the core prefetch reads a second copy of the label summary beside the hub")
	}
}

// The settled gate and label-summary owners behave as the screens rely on: a
// gate rejection resolves as an error, and a current hub snapshot is the only
// source until it is invalidated.
func TestGateAndLabelSummaryBehavior(t *testing.T) {
	runUISandbox(t, "gate_label_owner", gateLabelOwnerHarness, "gate and label owner behavior: ok")
}

const gateLabelOwnerHarness = `"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const dir = process.env.UI_DIR;
if (!dir) throw new Error("UI_DIR is required");
const app = fs.readFileSync(path.join(dir, "app.js"), "utf8");

function source(signature) {
  const start = app.indexOf(signature);
  assert.ok(start >= 0, "missing " + signature);
  const end = app.indexOf("\n  }", start);
  assert.ok(end > start, "unterminated " + signature);
  return app.slice(start, end + 4);
}

const sandbox = { console, Promise };
sandbox.window = sandbox;
vm.createContext(sandbox);

sandbox.settleRead = async (promise) => {
  try { return { value: await promise, error: null }; }
  catch (error) { return { value: null, error }; }
};
let gateFails = true;
sandbox.loadGate = async () => {
  if (gateFails) throw new Error("synthetic gate failure");
  return { known: true, open: true, preview: false };
};
vm.runInContext(source("function readGate() {"), sandbox, { filename: "app.js#readGate" });

(async () => {
  const failed = await sandbox.readGate();
  assert.equal(failed.value, null);
  assert.match(String(failed.error), /synthetic gate failure/);

  gateFails = false;
  const read = await sandbox.readGate();
  assert.equal(read.value.open, true);
  assert.equal(read.error, null);

  let hubCurrent = true;
  let summaryCalls = 0;
  sandbox.state = { hub: null };
  sandbox.CACHE_TTL = { labelsSummary: 1 };
  sandbox.cached = (pathName) => (pathName === "/api/hub" && hubCurrent ? { ok: true } : undefined);
  sandbox.requestCached = async (pathName) => {
    assert.equal(pathName, "/api/labels/summary");
    summaryCalls += 1;
    return { review: 7, manual: 1, judge: 0, needed: 3, questions: [] };
  };
  vm.runInContext(source("function labelSummarySnapshot(allowStale = false) {"), sandbox, { filename: "app.js#labelSummarySnapshot" });
  vm.runInContext(source("function readLabelSummary() {"), sandbox, { filename: "app.js#readLabelSummary" });

  sandbox.state.hub = {
    gate: {
      labels: { review: 4, manual: 2, judge: 0, needed: 5 },
      questions: [{ question: "category", review: 4 }],
    },
  };
  const fromHub = await sandbox.readLabelSummary();
  assert.equal(fromHub.value.review, 4);
  assert.equal(fromHub.value.questions.length, 1);
  assert.equal(summaryCalls, 0, "a current hub snapshot must not read the summary endpoint");

  hubCurrent = false;
  assert.equal(sandbox.labelSummarySnapshot(), null, "an invalidated hub is not a screen source");
  assert.equal(sandbox.labelSummarySnapshot(true).review, 4, "the nav badge may keep the last hub count");
  const invalidated = await sandbox.readLabelSummary();
  assert.equal(invalidated.value.review, 7);
  assert.equal(summaryCalls, 1, "an invalidated hub snapshot falls through to the summary read");

  sandbox.state.hub = null;
  hubCurrent = true;
  const noHub = await sandbox.readLabelSummary();
  assert.equal(noHub.value.review, 7);
  assert.equal(summaryCalls, 2);

  console.log("gate and label owner behavior: ok");
})().catch((error) => { console.error(error); process.exit(1); });
`
