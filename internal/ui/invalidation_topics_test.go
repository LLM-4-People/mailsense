package ui

import (
	"regexp"
	"slices"
	"strings"
	"testing"
)

// topicBody returns one entry of the invalidation topic graph. The graph is
// the single owner of what a live data change invalidates, so tests read its
// entries rather than re-spelling the lists.
func topicBody(t *testing.T, js, name string) string {
	t.Helper()
	start := strings.Index(js, "\n    "+name+": {")
	if start < 0 {
		t.Fatalf("invalidation topic %q not found", name)
	}
	rest := js[start:]
	end := strings.Index(rest, "\n    },")
	if end <= 0 {
		t.Fatalf("invalidation topic %q has no end", name)
	}
	return rest[:end]
}

// jsStringArrayField reads one string-array property from a topic body.
func jsStringArrayField(t *testing.T, body, field string) []string {
	t.Helper()
	match := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(field) + `: \[([^\]]*)\]`).FindStringSubmatch(body)
	if match == nil {
		t.Fatalf("%s array not found", field)
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

func sameStrings(left, right []string) bool {
	a := append([]string(nil), left...)
	b := append([]string(nil), right...)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

// A label or judge change owns labels, answers, the Hub's label gate and the
// learning status. An index run owns search plus the embeddings stage. Both
// scopes are exact: a broader set rebuilds screens the change cannot affect.
func TestInvalidationTopicsOwnLabelsAndIndexScope(t *testing.T) {
	shell := readUIModule(t, "13-shell.js")
	for _, tc := range []struct {
		name   string
		reads  []string
		routes []string
	}{
		{
			name:   "labels",
			reads:  []string{"/api/labels", "/api/answers", "/api/hub", "/api/learning", "/api/gate"},
			routes: []string{"labels", "answers", "hub", "learning"},
		},
		{
			name:   "index",
			reads:  []string{"/api/search", "/api/learning", "/api/hub"},
			routes: []string{"search", "learning", "hub"},
		},
	} {
		body := topicBody(t, shell, tc.name)
		if got := jsStringArrayField(t, body, "reads"); !sameStrings(got, tc.reads) {
			t.Errorf("%s reads = %v, want %v", tc.name, got, tc.reads)
		}
		if got := jsStringArrayField(t, body, "routes"); !sameStrings(got, tc.routes) {
			t.Errorf("%s routes = %v, want %v", tc.name, got, tc.routes)
		}
	}

	labels := topicBody(t, shell, "labels")
	for _, forbidden := range []string{
		"/api/brief", "/api/companies", "/api/priority", "/api/conversations",
		"/api/commitments", "/api/threads", "/api/triage", "/api/questions",
	} {
		if strings.Contains(labels, `"`+forbidden+`"`) {
			t.Errorf("the labels topic still clears %s", forbidden)
		}
	}
	for _, route := range jsStringArrayField(t, labels, "routes") {
		switch route {
		case "brief", "companies", "priority", "conversations", "triage", "questions", "mail":
			t.Errorf("the labels topic still stales %s", route)
		}
	}

	index := topicBody(t, shell, "index")
	for _, forbidden := range []string{"/api/messages", "/api/companies", "/api/brief"} {
		if strings.Contains(index, `"`+forbidden) {
			t.Errorf("the index topic still clears %s", forbidden)
		}
	}
	for _, route := range jsStringArrayField(t, index, "routes") {
		if route == "mail" || route == "companies" || route == "brief" {
			t.Errorf("the index topic still stales %s", route)
		}
	}
}

// The graph is the reactor: a topic derives the cache reads, the stale (or
// dropped) screens, the coalesced refresh and the local-model clears.
func TestInvalidationTopicsGraphBehavior(t *testing.T) {
	runUISandbox(t, "invalidation_topics", invalidationTopicsHarness, "invalidation topics behavior: ok")
}

const invalidationTopicsHarness = `"use strict";

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
  state: { questions: { questions: [{ name: "q" }] } },
  calls,
  invalidateCache: (prefixes) => calls.cache.push(prefixes),
  staleRouteCache: (...names) => calls.stale.push(names),
  invalidateRouteCache: (...names) => calls.drop.push(names),
  scheduleDataRefresh: (...names) => calls.refresh.push(names),
  forgetAllMail: () => { calls.forget += 1; },
};
vm.createContext(sandbox);
const script = app.slice(start, end) + "\n" +
  block("function eventCount(") + "\n" +
  block("function indexCommitted(") + "\n" +
  block("function invalidateTopics(") + "\nINVALIDATION_TOPICS;";
const topics = vm.runInContext(script, sandbox, { filename: "app.js#topics" });

function reset() {
  calls.cache = [];
  calls.stale = [];
  calls.drop = [];
  calls.refresh = [];
  calls.forget = 0;
}

// The stub receives arrays created in the VM realm; compare their JSON form so
// the assertion is about the values, not the realm's prototypes.
function same(actual, expected, message) {
  assert.equal(JSON.stringify(actual), JSON.stringify(expected), message);
}

function sorted(values) {
  return Array.from(values).sort();
}

assert.deepEqual(sorted(topics.labels.reads), sorted([
  "/api/labels", "/api/answers", "/api/hub", "/api/learning", "/api/gate",
]));
assert.deepEqual(sorted(topics.labels.routes), sorted(["labels", "answers", "hub", "learning"]));
assert.deepEqual(sorted(topics.index.reads), sorted(["/api/search", "/api/learning", "/api/hub"]));
assert.deepEqual(sorted(topics.index.routes), sorted(["search", "learning", "hub"]));

reset();
sandbox.invalidateTopics(["labels"]);
same(calls.cache, [["/api/labels", "/api/answers", "/api/hub", "/api/learning", "/api/gate"]]);
same(calls.stale, [["labels", "answers", "hub", "learning"]]);
same(calls.refresh, [["answers", "hub", "learning"]]);
same(calls.drop, []);
assert.equal(calls.forget, 0, "a label change dropped the All-Mail model");
assert.notEqual(sandbox.state.questions, null, "a label change dropped the question catalogue");

reset();
sandbox.invalidateTopics(["labels"], { hard: true });
same(calls.drop, [["labels", "answers", "hub", "learning"]]);
same(calls.stale, [], "a hard invalidation still marked screens stale");

reset();
sandbox.invalidateTopics(["index"]);
same(calls.cache, [["/api/search", "/api/learning", "/api/hub"]]);
same(calls.stale, [["search", "learning", "hub"]]);
same(calls.refresh, [["search", "learning", "hub"]]);

reset();
sandbox.invalidateTopics(["route"]);
same(calls.cache, [], "the route topic touched the API cache");
same(calls.stale, [[]], "the route topic did not stale every screen");

reset();
sandbox.invalidateTopics(["gate"], { hard: true });
same(calls.cache, [], "the gate topic touched the API cache");
same(calls.drop, [["hub", "brief", "learning", "mail", "companies", "labels"]]);

reset();
sandbox.state.questions = { questions: [] };
sandbox.invalidateTopics(["learningStatus"], { routes: false, refresh: false });
assert.equal(calls.cache.length, 1, "the status topic did not invalidate its reads");
assert.ok(calls.cache[0].includes("/api/jobs"), "the status topic lost the jobs read");
assert.ok(calls.cache[0].includes("/api/threads"), "the status topic lost the thread read");
same(calls.stale, []);
same(calls.refresh, []);

reset();
sandbox.invalidateTopics(["mail"]);
assert.equal(calls.forget, 1, "a sync topic did not drop the All-Mail model");
assert.ok(calls.cache[0].includes("/api/messages?"), "the sync topic lost the list read");

reset();
assert.throws(() => sandbox.invalidateTopics(["nope"]), /unknown invalidation topic/);

assert.equal(sandbox.indexCommitted(null), false, "a missing report claimed committed data");
assert.equal(sandbox.indexCommitted({ embedded: 0, unchanged: 0 }), false, "an empty run claimed committed data");
assert.equal(sandbox.indexCommitted({ embedded: 1, unchanged: 0 }), true, "an embedded vector was not committed");
assert.equal(sandbox.indexCommitted({ embedded: 0, unchanged: 2 }), true, "a confirmed vector was not committed");
assert.equal(sandbox.indexCommitted({ embedded: "bad", unchanged: -1 }), false, "a malformed report claimed committed data");

console.log("invalidation topics behavior: ok");
`

// afterLabelWrite (local save) and feedback.label (live event) are the same
// change and must share the labels topic; neither may fall back to the broad
// learning reaction.
func TestLabelWriteAndEventUseTheLabelsTopic(t *testing.T) {
	shell := readUIModule(t, "13-shell.js")
	labels := readUIModule(t, "26-labels.js")
	after := functionBody(t, labels, "function afterLabelWrite(")
	if !strings.Contains(after, `invalidateTopics(["labels"]`) {
		t.Fatal("afterLabelWrite does not use the labels topic")
	}
	if !strings.Contains(after, "{ hard: true") {
		t.Fatal("afterLabelWrite leaves the completed workbench cached")
	}
	for _, forbidden := range []string{"invalidateLearningReads(", "staleRouteCache(", "invalidateRouteCache(", "invalidateLearningDerived("} {
		if strings.Contains(after, forbidden) {
			t.Errorf("afterLabelWrite still spells its own invalidation via %q", forbidden)
		}
	}
	event := handlerBody(t, shell, `source.addEventListener("feedback.label"`)
	if !strings.Contains(event, `invalidateTopics(["labels"])`) {
		t.Fatal("feedback.label does not use the labels topic")
	}
	if strings.Contains(event, "afterLearningDataChanged") {
		t.Fatal("feedback.label still uses the broad learning reaction")
	}
	for _, handler := range []string{
		`source.addEventListener("evidence.finished"`,
		`source.addEventListener("layer1.finished"`,
	} {
		if !strings.Contains(handlerBody(t, shell, handler), "afterLearningDataChanged()") {
			t.Errorf("%s no longer refreshes the learning-derived views", handler)
		}
	}
}

// index.finished mirrors index.Report.Committed: only a run that stored or
// confirmed a vector changed search data. Its stage status changes on every
// finished run, so learning and Hub are revalidated even when uncommitted.
func TestIndexFinishedHonorsCommittedReport(t *testing.T) {
	shell := readUIModule(t, "13-shell.js")
	handler := handlerBody(t, shell, `source.addEventListener("index.finished"`)
	if !strings.Contains(handler, "indexCommitted(report)") {
		t.Fatal("index.finished does not honor Report.Committed")
	}
	if !strings.Contains(handler, `? "index" : "indexStatus"`) {
		t.Fatal("index.finished does not split committed search data from stage status")
	}
	committed := functionBody(t, shell, "function indexCommitted(")
	for _, want := range []string{"eventCount(report && report.embedded)", "eventCount(report && report.unchanged)"} {
		if !strings.Contains(committed, want) {
			t.Errorf("indexCommitted lacks %q", want)
		}
	}
}

// The company profile warm buffer is derived from the /api/companies reads.
// The cache choke point owns it, so no caller can drop the read and leave a
// profile warm for the next click.
func TestCompanyProfileWarmDropsWithCompaniesCache(t *testing.T) {
	runUISandbox(t, "company_profile_warm", companyProfileWarmHarness, "company profile warm behavior: ok")

	js := readApp(t)
	cache := functionBody(t, js, "function invalidateCache(")
	if !strings.Contains(cache, `if (invalidated("/api/companies")) companyProfileWarm.clear();`) {
		t.Fatal("invalidateCache does not clear the warm profile with /api/companies")
	}
	if strings.Index(cache, `invalidated("/api/priority")`) > strings.Index(cache, `invalidated("/api/companies")`) {
		t.Fatal("the company warm clear is not beside the other derived-model clears")
	}
}

const companyProfileWarmHarness = `"use strict";

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

const script = [
  "const apiCache = new Map();",
  "const apiInflight = new Map();",
  "let apiEpoch = 0;",
  "const companyProfileWarm = new Map();",
  "const state = { conversationPages: new Map(), conversationLoads: new Map() };",
  "function forgetPriorityPages() {}",
  "function forgetTriagePage() {}",
  "const CACHE_TTL = { companies: 15000 };",
  block("function invalidateCache("),
  block("function warmCompanyProfile("),
  block("function warmedCompanyProfile("),
].join("\n");

const sandbox = {};
vm.createContext(sandbox);
vm.runInContext(script, sandbox, { filename: "app.js#company-warm" });

sandbox.warmCompanyProfile("acme", { key: "acme", name: "Acme" });
assert.equal(sandbox.warmedCompanyProfile("acme").name, "Acme", "the warm profile did not warm");

sandbox.invalidateCache(["/api/labels"]);
assert.equal(sandbox.warmedCompanyProfile("acme").name, "Acme", "an unrelated prefix dropped the warm profile");

sandbox.invalidateCache(["/api/companies"]);
assert.equal(sandbox.warmedCompanyProfile("acme"), undefined, "invalidating /api/companies left the profile warm");

sandbox.warmCompanyProfile("acme", { key: "acme", name: "Acme" });
sandbox.invalidateCache(["/api/companies/acme?months=6&event_days=30"]);
assert.equal(sandbox.warmedCompanyProfile("acme"), undefined, "a company profile prefix left the profile warm");

sandbox.warmCompanyProfile("acme", { key: "acme", name: "Acme" });
sandbox.invalidateCache(["/api/companies?limit=20"]);
assert.equal(sandbox.warmedCompanyProfile("acme"), undefined, "a company list prefix left the profile warm");

console.log("company profile warm behavior: ok");
`
