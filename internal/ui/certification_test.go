package ui

import (
	"io/fs"
	"strings"
	"testing"
)

// The certification copy names the actual question count in both languages.
// The coverage wording says "Judged" because those rows include abstentions.
func TestCertificationHonestyCopy(t *testing.T) {
	en := readCatalog(t, "i18n.en.js")
	fr := readCatalog(t, "i18n.fr.js")
	cases := []struct {
		key string
		en  string
		fr  string
	}{
		{"learning.title.partial", "Certification in progress", "Certification en cours"},
		{
			"learning.meaning.partial",
			"{certified} of {total} questions are certified at the target. The others abstain until they certify.",
			"Questions certifiées à la cible : {certified} sur {total}. Les autres s'abstiennent jusqu'à leur certification.",
		},
		{"learning.certified_count", "{certified} of {total} certified", "{certified} sur {total} certifiées"},
		{"triage.partial_route", "{certified} of {total} certified", "{certified} sur {total} certifiées"},
		{"triage.route_unknown", "Certification unknown", "Certification inconnue"},
		{"triage.coverage", "Judged {fresh}/{eligible}", "Jugés {fresh}/{eligible}"},
		{"triage.coverage.fresh", "Judged", "Jugés"},
		{"mail.coverage_details", "Judged rows", "Lignes jugées"},
		{
			"mail.coverage_note",
			"Judged rows have a stored answer or abstention; certification is tracked separately.",
			"Les lignes jugées ont une réponse ou une abstention enregistrée; la certification est suivie séparément.",
		},
		{
			"mail.coverage_unavailable",
			"Coverage is unavailable until the triage summary can be read.",
			"La couverture est indisponible tant que le résumé de tri n’a pas pu être lu.",
		},
		{
			"hub.open.certified",
			"Questions certified at the target: {certified} of {total}; the others abstain until they certify.",
			"Questions certifiées à la cible : {certified} sur {total}; les autres s'abstiennent jusqu'à leur certification.",
		},
	}
	for _, tc := range cases {
		if got := en[tc.key].text; got != tc.en {
			t.Errorf("i18n.en.js %s = %q, want %q", tc.key, got, tc.en)
		}
		if got := fr[tc.key].text; got != tc.fr {
			t.Errorf("i18n.fr.js %s = %q, want %q", tc.key, got, tc.fr)
		}
	}
	// The old wording overstated route-row coverage as certification.
	if en["triage.coverage.fresh"].text == "Fresh" || fr["triage.coverage.fresh"].text == "À jour" {
		t.Error("the coverage strip still calls judged rows fresh")
	}
}

// The route's certification read owns the wait for the shared Hub snapshot:
// a failed read is unknown, never the previous certified count.
func TestReadCertificationAwaitsTheHubSnapshot(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	start := strings.Index(js, "function gateCertification(")
	end := strings.Index(js[start:], "// readGate refreshes")
	if start < 0 || end <= 0 {
		t.Fatal("gateCertification/readCertification not found")
	}
	if !strings.Contains(js[start:start+end], "async function readCertification(") ||
		!strings.Contains(js[start:start+end], "await loadHub()") {
		t.Fatal("readCertification does not await the shared Hub read")
	}
	runUISandbox(t, "read_certification", readCertificationHarness, "read certification behavior: ok")
}

const readCertificationHarness = `"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const dir = process.env.UI_DIR;
if (!dir) throw new Error("UI_DIR is required");

const sandbox = {
  state: { hub: null },
  loadHub: async () => { throw new Error("hub unavailable"); },
};
vm.createContext(sandbox);
const app = fs.readFileSync(path.join(dir, "app.js"), "utf8");
const start = app.indexOf("function gateCertification(");
const end = app.indexOf("// readGate refreshes", start);
assert.ok(start >= 0 && end > start, "gateCertification/readCertification is missing");
vm.runInContext(app.slice(start, end), sandbox, { filename: "app.js#readCertification" });
const readCertification = vm.runInContext("readCertification", sandbox);

(async () => {
  assert.equal(await readCertification(), null, "a failed Hub read is not unknown");

  let loads = 0;
  sandbox.loadHub = async () => {
    loads += 1;
    sandbox.state.hub = { gate: { certified: 1, total: 17 } };
  };
  let snapshot = await readCertification();
  assert.equal(loads, 1, "the certification read did not wait for the Hub");
  assert.equal(snapshot.certified, 1);
  assert.equal(snapshot.total, 17);

  sandbox.state.hub = { gate: { certified: 17, total: 17 } };
  sandbox.loadHub = async () => { throw new Error("hub unavailable"); };
  assert.equal(await readCertification(), null, "a failed read reused a stale certified snapshot");

  console.log("read certification behavior: ok");
})().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
`

// Every pill kind the shell emits has a style. In particular the certified
// (accent) and partial (warn) route pills must not look identical.
func TestPillKindsHaveStyles(t *testing.T) {
	css, err := fs.ReadFile(Files, "app.css")
	if err != nil {
		t.Fatal(err)
	}
	body := string(css)
	for _, kind := range []string{"accent", "warn", "done", "running", "blocked", "pending", "open", "closed", "preview", "no-dot"} {
		if !strings.Contains(body, ".pill."+kind) {
			t.Errorf("app.css lacks a .pill.%s rule", kind)
		}
	}
	for _, required := range []string{
		".pill.accent { background: var(--accent-soft); border-color: var(--accent-line); color: var(--accent-ink); }",
		".pill.warn { background: var(--warn-soft); border-color: var(--warn-line); color: var(--warn-ink); }",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("app.css lacks the pill variant %q", required)
		}
	}
}

// A gate can be open before every question is certified: the Learning screen
// names the progress and the count instead of claiming everything is ready.
func TestLearningCertificationPartialState(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	start := strings.Index(js, "async function renderLearning(")
	end := strings.Index(js, "function learningActions(")
	if start < 0 || end <= start {
		t.Fatal("renderLearning not found")
	}
	body := js[start:end]
	for _, required := range []string{
		"readCertification(),",
		"const partial = !!(open && certification && certification.certified < certification.total)",
		`const titleValue = partial ? "partial" : stateValue`,
		`learningTitle(titleValue)`, `learningMeaning(titleValue, certificationParams)`,
		`t("learning.certified_count", certificationParams)`,
		`learningStateText(partial ? "partial" : stateValue)`,
		`partial ? "warn"`,
		`certification.certified >= certification.total ? "accent" : "warn"`,
	} {
		if !strings.Contains(body, required) {
			t.Errorf("renderLearning lacks the partial certification step %q", required)
		}
	}
	runUISandbox(t, "learning_certification", learningCertificationHarness, "learning certification behavior: ok")
}

const learningCertificationHarness = `"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const dir = process.env.UI_DIR;
if (!dir) throw new Error("UI_DIR is required");

const sandbox = {
  navigator: { languages: ["en-US"], language: "en-US" },
  document: { documentElement: { lang: "", dataset: {} }, title: "" },
  console,
};
sandbox.window = sandbox;
vm.createContext(sandbox);
for (const file of ["i18n.en.js", "i18n.fr.js", "i18n.js"]) {
  vm.runInContext(fs.readFileSync(path.join(dir, file), "utf8"), sandbox, { filename: file });
}
const I = sandbox.window.MailsenseI18n;
if (!I) throw new Error("i18n runtime missing");
I.setLanguage({ language: "en", formatLocale: "en-US" });
sandbox.I18N = I;
sandbox.t = I.t.bind(I); sandbox.metaLine = I.metaLine; sandbox.metaSeparator = I.metaSeparator;
sandbox.tOr = I.tOr.bind(I);
sandbox.formatNumber = I.formatNumber.bind(I);
sandbox.formatPercent = () => "";
sandbox.formatRelative = () => "";
sandbox.formatDate = () => "";
sandbox.statusLabel = (value) => String(value || "");
sandbox.humanCode = (value) => String(value || "");

function textNode(value) {
  return { tag: "#text", text: String(value), attrs: {}, children: [] };
}
function makeNode(tag, attrs, children) {
  const node = {
    tag,
    attrs: {}, children: [], text: "", dataset: {}, value: "", hidden: false,
    textContent: "", className: "",
    addEventListener() {}, focus() {}, setAttribute(name, value) { this.attrs[name] = value; },
    append(...kids) { kids.forEach(add); },
  };
  function add(child) {
    if (child === null || child === undefined || child === false) return;
    if (Array.isArray(child)) { child.forEach(add); return; }
    if (child && typeof child === "object" && child.tag) { node.children.push(child); return; }
    node.children.push(textNode(child));
  }
  for (const [key, value] of Object.entries(attrs || {})) {
    if (key === "dataset") { Object.assign(node.dataset, value || {}); continue; }
    if (key === "class") { node.className = String(value); continue; }
    if (key === "text") { node.text = String(value); node.textContent = String(value); }
    if (key === "hidden") node.hidden = !!value;
    node.attrs[key] = value;
  }
  (children || []).forEach(add);
  return node;
}
sandbox.h = (tag, attrs, ...children) => makeNode(tag, attrs, children);
sandbox.pill = (value, kind = "") => makeNode("span", { class: "pill " + kind, text: value });
sandbox.gatePill = (open) => makeNode("span", {
  class: "pill " + (open ? "open" : "closed"),
  text: open ? "Gate open" : "Gate closed",
});
sandbox.previewPill = () => makeNode("span", { class: "pill preview", text: "Preview" });
sandbox.warningBanner = (...children) => makeNode("section", { class: "banner" }, children);
sandbox.icon = () => null;
sandbox.routeHash = (name) => "#" + name;
sandbox.questionTitle = (name) => String(name);
sandbox.questionHelp = () => null;
sandbox.kv = (pairs) => makeNode("dl", null, pairs.filter(Boolean).map(([key, value]) =>
  makeNode("div", null, [textNode(key), textNode(value)])));
sandbox.button = (label) => makeNode("button", { text: label });
sandbox.link = (label) => makeNode("a", { text: label });
sandbox.section = (title, sub, actions, body) => makeNode("section", null, [title, sub, body]);
sandbox.stageNode = () => null;
sandbox.blockerNode = () => null;
sandbox.emptyState = (title, body) => makeNode("div", { class: "empty" }, [title, body]);
sandbox.learningActions = () => [];
sandbox.jobSnapshot = () => [];
sandbox.allBlockers = () => [];
sandbox.loadLearning = async () => ({ status: { state: "blocked", stages: [] } });
sandbox.readLabelSummary = async () => ({ value: { review: 0, questions: [] }, error: null });
// The certification read uses the real gate snapshot; a no-op Hub load keeps
// each case's state.hub as the snapshot under test.
sandbox.loadHub = async () => {};
sandbox.navigate = () => {};
sandbox.refreshActiveRoute = () => {};
sandbox.state = {
  renderSeq: 1,
  gate: { known: true, open: true, preview: false },
  hub: { gate: { certified: 1, total: 17 } },
};

function texts(node, out = []) {
  if (!node) return out;
  if (node.tag === "#text") { out.push(node.text); return out; }
  if (node.text) out.push(String(node.text));
  if (node.attrs && node.attrs.text) out.push(String(node.attrs.text));
  for (const child of node.children || []) texts(child, out);
  return out;
}
const textOf = (node) => texts(node).join(" | ");

const app = fs.readFileSync(path.join(dir, "app.js"), "utf8");
function run(startMarker, endMarker, label) {
  const start = app.indexOf(startMarker);
  const end = app.indexOf(endMarker, start);
  assert.ok(start >= 0 && end > start, label + " markers are missing");
  vm.runInContext(app.slice(start, end), sandbox, { filename: "app.js#" + label });
}
run("function learningStateText(", "function reasonText(", "learning-labels");
run("function gateKnown()", "function readGate()", "gate-snapshot");
run("function certificationCounts(", "// learningActions is the callout's next-step list.", "learning-render");

(async () => {
const partial = textOf(await sandbox.renderLearning({}, 1));
assert.ok(partial.includes("Certification in progress"), "the partial title is missing: " + partial);
assert.ok(partial.includes("1 of 17 questions are certified at the target. The others abstain until they certify."),
  "the partial meaning is missing: " + partial);
assert.ok(partial.includes("1 of 17 certified"), "the certification count is missing: " + partial);
assert.ok(partial.includes("Partly certified"), "the partial state pill is missing: " + partial);
assert.ok(!partial.includes("Ready"), "a partial gate still claims it is ready: " + partial);
assert.ok(!partial.includes("Everything is ready"), "a partial gate claims everything is ready");

sandbox.state.hub = { gate: { certified: 17, total: 17 } };
const full = textOf(await sandbox.renderLearning({}, 1));
assert.ok(full.includes("Everything is ready"), "a full gate does not say it is ready: " + full);
assert.ok(full.includes("17 of 17 certified"), "a full gate lacks the count: " + full);
assert.ok(full.includes("Ready"), "a full gate lacks the Ready state pill: " + full);
assert.ok(!full.includes("Partly certified"), "a full gate still claims partial certification: " + full);
assert.ok(!full.includes("Certification in progress"), "a full gate claims certification is in progress");

sandbox.state.hub = null;
const unknown = textOf(await sandbox.renderLearning({}, 1));
assert.ok(!unknown.includes(" of 17 certified"), "an unknown count invented a certification: " + unknown);
assert.ok(!unknown.includes("Partly certified"), "an unknown count invented partial certification: " + unknown);

I.setLanguage({ language: "fr", formatLocale: "fr-FR" });
sandbox.state.hub = { gate: { certified: 1, total: 17 } };
const french = textOf(await sandbox.renderLearning({}, 1));
assert.ok(french.includes("Certification en cours"), "the French partial title is missing: " + french);
assert.ok(french.includes("Questions certifiées à la cible : 1 sur 17. Les autres s'abstiennent jusqu'à leur certification."),
  "the French partial meaning is missing: " + french);
assert.ok(french.includes("1 sur 17 certifiées"), "the French count is missing: " + french);
assert.ok(french.includes("Certification partielle"), "the French partial state pill is missing: " + french);
assert.ok(!french.includes("Prêt"), "a partial gate still claims it is ready: " + french);
assert.ok(!french.includes("1 questions"), "the French count has the wrong number grammar: " + french);

console.log("learning certification behavior: ok");
})().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
`

// The Recent coverage disclosure counts stored route rows, answers and
// abstentions alike, so its labels say "Judged" and the note separates
// certification from coverage. The numbers stay the same.
func TestRecentCoverageJudgedLabels(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	start := strings.Index(js, "function recentCoverageDetails(")
	end := strings.Index(js, "async function renderRecent(")
	if start < 0 || end <= start {
		t.Fatal("recentCoverageDetails not found")
	}
	body := js[start:end]
	for _, required := range []string{
		`t("mail.coverage_details")`, `t("mail.coverage_note")`,
		`t("triage.coverage.fresh")`, `t("triage.coverage.eligible")`,
		`t("triage.summary.preview_note")`, `t("mail.coverage_unavailable")`,
	} {
		if !strings.Contains(body, required) {
			t.Errorf("recentCoverageDetails lacks %q", required)
		}
	}
	summaryStart := strings.Index(js, "async function ensureTriageView(")
	summaryEnd := strings.Index(js[summaryStart:], "return view;")
	if summaryStart < 0 || summaryEnd <= 0 {
		t.Fatal("ensureTriageView not found")
	}
	summaryBody := js[summaryStart : summaryStart+summaryEnd]
	for _, required := range []string{
		`settleRead(requestCached("/api/triage/summary"`,
		`state.triageSummaryError = summaryRead.error`,
		`state.triageSummary = summaryRead.value`,
	} {
		if !strings.Contains(summaryBody, required) {
			t.Errorf("ensureTriageView lacks the settled summary read %q", required)
		}
	}
	recentStart := end
	recentEnd := strings.Index(js[recentStart:], "async function loadMoreTriage")
	if recentEnd <= 0 {
		t.Fatal("renderRecent end not found")
	}
	recentBody := js[recentStart : recentStart+recentEnd]
	for _, required := range []string{
		`const coverage = summary && summary.coverage`,
		`const coverageKind = !coverage ? ""`,
		`coverage ? pill(t("triage.coverage"`,
		`routePill(view ? view.routed : null, preview, certification)`,
		`readCertification()`,
	} {
		if !strings.Contains(recentBody, required) {
			t.Errorf("renderRecent lacks the unknown-coverage rule %q", required)
		}
	}
	if !strings.Contains(js, "const coverageDetails = recentCoverageDetails(summary, coverage);") {
		t.Error("renderRecent does not use the one coverage disclosure")
	}
	runUISandbox(t, "coverage_details", coverageDetailsHarness, "coverage detail behavior: ok")
}

const coverageDetailsHarness = `"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const dir = process.env.UI_DIR;
if (!dir) throw new Error("UI_DIR is required");

const sandbox = {
  navigator: { languages: ["en-US"], language: "en-US" },
  document: { documentElement: { lang: "", dataset: {} }, title: "" },
  console,
};
sandbox.window = sandbox;
vm.createContext(sandbox);
for (const file of ["i18n.en.js", "i18n.fr.js", "i18n.js"]) {
  vm.runInContext(fs.readFileSync(path.join(dir, file), "utf8"), sandbox, { filename: file });
}
const I = sandbox.window.MailsenseI18n;
if (!I) throw new Error("i18n runtime missing");
I.setLanguage({ language: "en", formatLocale: "en-US" });
sandbox.t = I.t.bind(I); sandbox.metaLine = I.metaLine; sandbox.metaSeparator = I.metaSeparator;
sandbox.formatNumber = I.formatNumber.bind(I);

function textNode(value) {
  return { tag: "#text", text: String(value), attrs: {}, children: [] };
}
function makeNode(tag, attrs, children) {
  const node = { tag, attrs: {}, children: [], text: "", dataset: {}, className: "" };
  function add(child) {
    if (child === null || child === undefined || child === false) return;
    if (Array.isArray(child)) { child.forEach(add); return; }
    if (child && typeof child === "object" && child.tag) { node.children.push(child); return; }
    node.children.push(textNode(child));
  }
  for (const [key, value] of Object.entries(attrs || {})) {
    if (key === "class") { node.className = String(value); continue; }
    if (key === "text") node.text = String(value);
    node.attrs[key] = value;
  }
  (children || []).forEach(add);
  return node;
}
sandbox.h = (tag, attrs, ...children) => makeNode(tag, attrs, children);
sandbox.statStrip = (items) => makeNode("div", { class: "stat-strip" },
  items.map((item) => makeNode("div", { class: "stat" }, [
    makeNode("span", { class: "stat-label", text: item.label }),
    makeNode("strong", { class: "stat-value", text: item.value }),
  ])));

function texts(node, out = []) {
  if (!node) return out;
  if (node.tag === "#text") { out.push(node.text); return out; }
  if (node.text) out.push(String(node.text));
  if (node.attrs && node.attrs.text) out.push(String(node.attrs.text));
  for (const child of node.children || []) texts(child, out);
  return out;
}

const app = fs.readFileSync(path.join(dir, "app.js"), "utf8");
const start = app.indexOf("function recentCoverageDetails(");
const end = app.indexOf("async function renderRecent(", start);
assert.ok(start >= 0 && end > start, "recentCoverageDetails is missing");
vm.runInContext(app.slice(start, end), sandbox, { filename: "app.js#recentCoverageDetails" });
const details = vm.runInContext("recentCoverageDetails", sandbox);

const text = texts(details({ preview: false }, {
  eligible: 17, fresh: 1, incomplete: 2, stale: 3, failed: 4, unjudged: 5,
})).join(" | ");
assert.ok(text.includes("Judged rows"), "the disclosure does not say Judged rows: " + text);
assert.ok(text.includes("Judged rows have a stored answer or abstention; certification is tracked separately."),
  "the coverage note is missing: " + text);
assert.ok(text.includes("Judged"), "the fresh stat lost its Judged label: " + text);
assert.ok(text.includes("Eligible"), "the eligible stat is missing: " + text);
for (const value of ["17", "1", "2", "3", "4", "5"]) {
  assert.ok(text.includes(value), "the coverage count " + value + " is missing: " + text);
}
assert.ok(!text.includes("Coverage details"), "the old coverage wording is still rendered");

const unknown = texts(details({ preview: false }, null)).join(" | ");
assert.ok(unknown.includes("Coverage is unavailable until the triage summary can be read."),
  "unknown coverage lacks an explicit unavailable line: " + unknown);
assert.ok(!unknown.includes("Judged"), "unknown coverage was painted as zero rows: " + unknown);

const preview = texts(details({ preview: true }, {})).join(" | ");
assert.ok(preview.includes("These counts describe the certified route only."),
  "the preview coverage note is missing: " + preview);

console.log("coverage detail behavior: ok");
`

// The route pill is a count, not a route-existence badge. A partial snapshot
// and an unloaded Hub never borrow the certified wording, and the preview
// still wins over every count.
const routePillCertificationHarness = `"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const dir = process.env.UI_DIR;
if (!dir) throw new Error("UI_DIR is required");

const sandbox = {
  navigator: { languages: ["en-US"], language: "en-US" },
  document: { documentElement: { lang: "", dataset: {} }, title: "" },
  console,
};
sandbox.window = sandbox;
vm.createContext(sandbox);
for (const file of ["i18n.en.js", "i18n.fr.js", "i18n.js"]) {
  vm.runInContext(fs.readFileSync(path.join(dir, file), "utf8"), sandbox, { filename: file });
}
const I = sandbox.window.MailsenseI18n;
if (!I) throw new Error("i18n runtime missing");
sandbox.t = I.t.bind(I); sandbox.metaLine = I.metaLine; sandbox.metaSeparator = I.metaSeparator;
sandbox.formatNumber = I.formatNumber.bind(I);
sandbox.pill = (value, kind = "") => ({ text: String(value), kind });

const app = fs.readFileSync(path.join(dir, "app.js"), "utf8");
const start = app.indexOf("function routePill(");
const end = app.indexOf("\n  }", start);
assert.ok(start >= 0 && end > start, "routePill is missing");
vm.runInContext(app.slice(start, end + 4), sandbox, { filename: "app.js#routePill" });
const routePill = vm.runInContext("routePill", sandbox);
const call = (routed, preview, certification) => routePill(routed, preview, certification);
const textOf = (node) => node.text;
const kindOf = (node) => node.kind;

I.setLanguage({ language: "en", formatLocale: "en-US" });
assert.equal(textOf(call(false, false, { certified: 17, total: 17 })), "Paid decider");
assert.equal(kindOf(call(false, false, { certified: 17, total: 17 })), "pending");

// Preview wins over a full, a partial and an unknown count.
for (const certification of [{ certified: 17, total: 17 }, { certified: 1, total: 17 }, null]) {
  const node = call(true, true, certification);
  assert.equal(textOf(node), "Uncertified preview", "preview lost to a count");
  assert.equal(kindOf(node), "preview", "preview is not the warning style");
}

assert.equal(textOf(call(true, false, { certified: 17, total: 17 })), "Certified route");
assert.equal(kindOf(call(true, false, { certified: 17, total: 17 })), "accent");

const partial = call(true, false, { certified: 1, total: 17 });
assert.equal(textOf(partial), "1 of 17 certified");
assert.equal(kindOf(partial), "warn");
assert.ok(!textOf(partial).includes("Certified route"), "a partial count claims a certified route");

const unknown = call(true, false, null);
assert.equal(textOf(unknown), "Certification unknown");
assert.equal(kindOf(unknown), "pending");
assert.ok(!textOf(unknown).includes("Certified route"), "an unknown count claims a certified route");

I.setLanguage({ language: "fr", formatLocale: "fr-FR" });
assert.equal(textOf(call(true, false, { certified: 17, total: 17 })), "Route certifiée");
assert.equal(textOf(call(true, false, { certified: 1, total: 17 })), "1 sur 17 certifiées");
assert.equal(textOf(call(true, false, null)), "Certification inconnue");
assert.equal(textOf(call(true, true, { certified: 17, total: 17 })), "Aperçu non certifié");
const hubCopy = I.t("hub.open.certified", { certified: 1, total: 17 });
assert.equal(hubCopy, "Questions certifiées à la cible : 1 sur 17; les autres s'abstiennent jusqu'à leur certification.");
assert.ok(!hubCopy.includes("1 questions"), "the French Hub count has the wrong number grammar: " + hubCopy);

console.log("route pill certification: ok");
`

// TestGateCertificationSnapshot pins the one owner of the questions-stage
// count: no Hub or no questions is unknown, and the certified count can never
// exceed the total or go negative.
func TestGateCertificationSnapshot(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	start := strings.Index(js, "function gateCertification(")
	if start < 0 {
		t.Fatalf("app.js lacks gateCertification")
	}
	end := strings.Index(js[start:], "\n  }")
	if end < 0 {
		t.Fatalf("gateCertification has no end")
	}
	if !strings.Contains(js[start:start+end], "state.hub && state.hub.gate") {
		t.Fatal("gateCertification does not read the Hub gate")
	}
	runUISandbox(t, "gate_certification", gateCertificationHarness, "gate certification snapshot: ok")
}

const gateCertificationHarness = `"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const dir = process.env.UI_DIR;
if (!dir) throw new Error("UI_DIR is required");

const sandbox = { state: { hub: null } };
vm.createContext(sandbox);
const app = fs.readFileSync(path.join(dir, "app.js"), "utf8");
const start = app.indexOf("function gateCertification(");
const end = app.indexOf("\n  }", start);
assert.ok(start >= 0 && end > start, "gateCertification is missing");
vm.runInContext(app.slice(start, end + 4), sandbox, { filename: "app.js#gateCertification" });
const gateCertification = vm.runInContext("gateCertification", sandbox);

assert.equal(gateCertification(), null, "an unloaded Hub is not a certification");
sandbox.state.hub = {};
assert.equal(gateCertification(), null, "a Hub without a gate is not a certification");
sandbox.state.hub = { gate: { certified: 3, total: 0 } };
assert.equal(gateCertification(), null, "a stage without questions is not a certification");
sandbox.state.hub = { gate: { certified: 1, total: 17 } };
let snapshot = gateCertification();
assert.equal(snapshot.certified, 1);
assert.equal(snapshot.total, 17);
sandbox.state.hub = { gate: { certified: "2", total: "17" } };
snapshot = gateCertification();
assert.equal(snapshot.certified, 2, "a numeric certified string is not read");
assert.equal(snapshot.total, 17, "a numeric total string is not read");
sandbox.state.hub = { gate: { certified: 99, total: 17 } };
snapshot = gateCertification();
assert.equal(snapshot.certified, 17, "the count exceeds the total");
assert.equal(snapshot.total, 17);
sandbox.state.hub = { gate: { certified: -4, total: 17 } };
snapshot = gateCertification();
assert.equal(snapshot.certified, 0, "the count went negative");
assert.equal(snapshot.total, 17);
sandbox.state.hub = { gate: { certified: 4, total: "none" } };
assert.equal(gateCertification(), null, "an unreadable total is not a guess");

console.log("gate certification snapshot: ok");
`
