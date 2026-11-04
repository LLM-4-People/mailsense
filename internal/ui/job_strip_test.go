package ui

import (
	"io/fs"
	"strings"
	"testing"
)

// The top strip is one aggregate card, not a wrapping grid of chips. Only
// the summary owns a progress element.
func TestJobStripIsOneAggregate(t *testing.T) {
	jobs := readUIModule(t, "13-jobs.js")
	if strings.Count(jobs, `h("progress"`) != 1 {
		t.Fatalf("the job strip creates %d progress elements; want one", strings.Count(jobs, `h("progress"`))
	}
	for _, forbidden := range []string{".job-chip", "job-chip-name", "job-chip-meta"} {
		if strings.Contains(jobs, forbidden) {
			t.Errorf("13-jobs.js still owns the per-job chip %q", forbidden)
		}
	}
	for _, want := range []string{
		"function jobAggregate(jobs)",
		"function paintJobSummary(summary, jobs)",
		"function paintJobActions(host, jobs)",
		"paintJobActions(actions, jobs);",
		"meter.hidden = aggregate.running.length === 0;",
		"meter.removeAttribute(\"value\");",
		`t("shell.job.active", { count: aggregate.running.length })`,
		`t("shell.job.background")`,
		`t("shell.job.cancel_job"`,
		`t("shell.job.dismiss_job"`,
		"cancelJob(id, event.currentTarget)",
		"forgetJob(id);",
	} {
		if !strings.Contains(jobs, want) {
			t.Errorf("13-jobs.js lacks %q", want)
		}
	}

	css, err := fs.ReadFile(Files, "css/10-shell.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{".job-summary", ".job-aggregate", ".job-actions"} {
		if !strings.Contains(string(css), want) {
			t.Errorf("10-shell.css lacks %q", want)
		}
	}
	if strings.Contains(string(css), ".job-chip") {
		t.Error("10-shell.css still styles the removed per-job chip")
	}
}

// The aggregate is weighted by each running job's total. Finished jobs
// linger in the text but never pull the running bar backwards.
func TestJobAggregateFormula(t *testing.T) {
	runUISandbox(t, "job_aggregate", jobAggregateHarness, "job aggregate: ok")
}

// The one strip's summary and per-job actions are catalog copy in both
// languages; the plural lead and meter name keep their count placeholder.
func TestJobStripCopyIsLocalized(t *testing.T) {
	en, fr := readCatalog(t, "i18n.en.js"), readCatalog(t, "i18n.fr.js")
	for _, key := range []string{
		"shell.job.active",
		"shell.job.background",
		"shell.job.aggregate_aria",
		"shell.job.cancel_job",
		"shell.job.dismiss_job",
		"shell.job.failed_with_reason",
	} {
		if _, ok := en[key]; !ok {
			t.Errorf("i18n.en.js is missing %q", key)
		}
		if _, ok := fr[key]; !ok {
			t.Errorf("i18n.fr.js is missing %q", key)
		}
	}
	for _, key := range []string{"shell.job.active", "shell.job.aggregate_aria"} {
		for _, catalog := range []map[string]catalogEntry{en, fr} {
			entry := catalog[key]
			if !entry.isPlural {
				t.Errorf("%s must be plural", key)
				continue
			}
			for _, form := range []string{"one", "other"} {
				if !strings.Contains(entry.plural[form], "{count}") {
					t.Errorf("%s.%s lacks {count}", key, form)
				}
			}
		}
	}
	for _, key := range []string{"shell.job.cancel_job", "shell.job.dismiss_job", "shell.job.failed_with_reason"} {
		for _, catalog := range []map[string]catalogEntry{en, fr} {
			if !strings.Contains(catalog[key].text, "{name}") {
				t.Errorf("%s lacks {name}", key)
			}
		}
	}
}

const jobAggregateHarness = `"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const dir = process.env.UI_DIR;
if (!dir) throw new Error("UI_DIR is required");
const app = fs.readFileSync(path.join(dir, "app.js"), "utf8");
const start = app.indexOf("function jobAggregate(");
assert.ok(start >= 0, "jobAggregate not found");
const end = app.indexOf("\n  }", start);
assert.ok(end > start, "jobAggregate has no end");
const script = app.slice(start, end + 4) + "\njobAggregate;";
const scope = {};
vm.createContext(scope);
const jobAggregate = vm.runInContext(script, scope, { filename: "app.js#job-aggregate" });

const running = (id, done, total, extra) => Object.assign({
  id,
  status: "running",
  progress: { done, total, unit: "messages" },
}, extra || {});
const finished = (id, status, done, total) => ({
  id,
  status,
  progress: { done, total, unit: "messages" },
});

let aggregate = jobAggregate([
  running("a", 10, 100, { progress: { done: 10, total: 100, unit: "messages", eta: 60 } }),
  running("b", 30, 300, { progress: { done: 30, total: 300, unit: "messages", eta: 120 } }),
  finished("c", "succeeded", 500, 500),
]);
assert.equal(aggregate.done, 40, "summed done is wrong");
assert.equal(aggregate.total, 400, "summed total is wrong");
assert.ok(Math.abs(aggregate.percent - 0.1) < 1e-9, "weighted percent is wrong");
assert.equal(aggregate.indeterminate, false);
assert.equal(aggregate.unit, "messages");
assert.equal(aggregate.eta, 120, "the aggregate ETA is not the longest remaining time");
assert.equal(aggregate.etaKnown, true);

aggregate = jobAggregate([finished("c", "succeeded", 500, 500)]);
assert.equal(aggregate.done, 0, "a finished job contributed done");
assert.equal(aggregate.total, 0, "a finished job contributed total");
assert.equal(aggregate.percent, null);
assert.equal(aggregate.indeterminate, false, "a finished-only strip claims a running aggregate");
assert.equal(aggregate.etaKnown, false);

aggregate = jobAggregate([running("a", 2, 0), running("b", 1, 0)]);
assert.equal(aggregate.indeterminate, true, "two starting jobs are not indeterminate");
assert.equal(aggregate.total, 0);
assert.equal(aggregate.percent, null);

aggregate = jobAggregate([
  running("a", 2, 10, { progress: { done: 2, total: 10, unit: "messages" } }),
  running("b", 1, 0, { progress: { done: 1, total: 0, unit: "questions" } }),
]);
assert.equal(aggregate.total, 10, "a starting job was counted as a total");
assert.equal(aggregate.unit, "", "mixed counted and starting jobs claimed one unit");
assert.equal(aggregate.indeterminate, false);

aggregate = jobAggregate([running("a", 150, 100)]);
assert.equal(aggregate.done, 100, "done above total was not clamped");
assert.equal(aggregate.percent, 1);

aggregate = jobAggregate([
  running("a", 1, 10, { progress: { done: 1, total: 10, eta: 30 } }),
  running("b", 2, 20, { progress: { done: 2, total: 20 } }),
]);
assert.equal(aggregate.etaKnown, false, "an unknown job ETA was treated as known");
assert.equal(aggregate.eta, 0);

console.log("job aggregate: ok");
`

// A failed job names its cause and stays until the owner dismisses it: the
// strip must never report a failure with no reason, and it must not schedule
// an infinite linger as a timer that fires at once.
func TestFailedJobShowsItsReasonAndStays(t *testing.T) {
	jobs := readUIModule(t, "13-jobs.js")
	for _, want := range []string{
		`info.status === "failed" && info.error`,
		`t("shell.job.failed_with_reason", { name, reason: boundedReason(info.error) })`,
		"function boundedReason(reason)",
		"JOB_REASON_MAX_CHARS",
		`return info.status === "failed" ? Infinity : JOB_STRIP_LINGER_MS;`,
		"if (!Number.isFinite(wait)) {",
	} {
		if !strings.Contains(jobs, want) {
			t.Errorf("13-jobs.js lacks %q", want)
		}
	}
	core := readUIModule(t, "00-core.js")
	if strings.Contains(core, "JOB_STRIP_FAILED_MS") {
		t.Error("the failed-job linger is no longer a constant: failures stay until dismissed")
	}
}
