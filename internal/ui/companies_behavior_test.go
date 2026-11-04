package ui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runCompaniesBehavior executes the catalog-facing company helpers under Node
// with the real catalogs: zero-filled months, diacritic-insensitive search and
// friendly state labels are all pure data contracts.
func runCompaniesBehavior(t *testing.T) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed: source and catalog checks still run")
	}
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "companies_behavior_test.js")
	if err := os.WriteFile(script, []byte(companiesBehaviorHarness), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, script)
	cmd.Env = append(os.Environ(), "UI_DIR="+dir, "TZ=UTC")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("companies behavior timed out: %v\n%s", ctx.Err(), out)
	}
	if err != nil {
		t.Fatalf("companies behavior failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "companies behavior: ok") {
		t.Fatalf("companies behavior produced no success marker:\n%s", out)
	}
}

const companiesBehaviorHarness = `"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const dir = process.env.UI_DIR;
if (!dir) throw new Error("UI_DIR is required");

const sandbox = {
  navigator: { languages: ["en-US"], language: "en-US" },
  document: { documentElement: { lang: "", dataset: {} }, title: "" },
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
sandbox.tOr = I.tOr.bind(I);
sandbox.compareText = I.compareText.bind(I);
sandbox.COMPANIES_MONTHS = 12;
sandbox.THREAD_STATES = [
  "awaiting_owner", "awaiting_other", "answered_by_owner",
  "answered_by_other", "no_action", "unknown",
];

const app = fs.readFileSync(path.join(dir, "app.js"), "utf8");
const start = app.indexOf("// companies-view:start");
const end = app.indexOf("// companies-view:end");
assert.ok(start >= 0 && end > start, "companies-view markers are missing");
vm.runInContext(app.slice(start, end), sandbox, { filename: "app.js#companies-view" });

sandbox.COMPANIES_EVENT_DAYS = 30;

function h(tag, attrs, ...children) {
  const node = { tag, attrs: attrs || {}, children: [], text: "" };
  node.setAttribute = (name, value) => { node.attrs[name] = String(value); };
  node.getAttribute = (name) => node.attrs[name];
  node.addEventListener = () => {};
  const add = (child) => {
    if (child === null || child === undefined || child === false) return;
    if (Array.isArray(child)) { child.forEach(add); return; }
    if (child && typeof child === "object" && child.tag) { node.children.push(child); return; }
    node.children.push({ tag: "#text", text: String(child), attrs: {}, children: [] });
  };
  node.append = (...kids) => { kids.forEach(add); return node; };
  children.forEach(add);
  return node;
}
sandbox.h = h;
sandbox.text = (value) => ({ tag: "#text", text: String(value), attrs: {}, children: [] });
sandbox.fragment = (...children) => h("#fragment", {}, ...children);
sandbox.badge = (value, kind = "", title = "") => ({
  tag: "span",
  attrs: { class: "badge " + kind, title },
  text: String(value),
  children: [],
});
sandbox.formatNumber = (value) => String(value);
sandbox.formatRelative = () => "12 hours ago";
sandbox.formatDate = () => "Oct 26, 2026";
sandbox.formatMonth = (value) => "Month " + value;
sandbox.formatDuration = () => "30 minutes";
sandbox.mailHref = (id, mode = "important") => "#/mail?mode=" + mode + "&id=" + String(id);
sandbox.icon = () => ({ tag: "svg", attrs: {}, children: [], text: "" });
sandbox.button = (label) => ({ tag: "button", attrs: {}, text: String(label), children: [] });

const ss = app.indexOf("// stat-strip:start"), se = app.indexOf("// stat-strip:end");
assert.ok(ss >= 0 && se > ss, "stat-strip markers are missing"); vm.runInContext(app.slice(ss, se), sandbox, { filename: "app.js#stat-strip" });
const sh = app.indexOf("// section-head:start"), she = app.indexOf("// section-head:end");
assert.ok(sh >= 0 && she > sh, "section-head markers are missing"); vm.runInContext(app.slice(sh, she), sandbox, { filename: "app.js#section-head" });

const sortableStart = app.indexOf("// sortable:start");
const sortableEnd = app.indexOf("// sortable:end");
assert.ok(sortableStart >= 0 && sortableEnd > sortableStart, "sortable markers are missing");
vm.runInContext(app.slice(sortableStart, sortableEnd), sandbox, { filename: "app.js#sortable" });

const threadStateStart = app.indexOf("// thread-state:start");
const threadStateEnd = app.indexOf("// thread-state:end");
assert.ok(threadStateStart >= 0 && threadStateEnd > threadStateStart, "thread-state markers are missing");
vm.runInContext(app.slice(threadStateStart, threadStateEnd), sandbox, { filename: "app.js#thread-state" });

const renderStart = app.indexOf("// companies-render:start");
const renderEnd = app.indexOf("// companies-render:end");
assert.ok(renderStart >= 0 && renderEnd > renderStart, "companies-render markers are missing");
vm.runInContext(app.slice(renderStart, renderEnd), sandbox, { filename: "app.js#companies-render" });

function texts(node, out = []) {
  if (!node) return out;
  if (node.tag === "#text") { out.push(node.text); return out; }
  if (node.text) out.push(String(node.text));
  if (node.attrs && node.attrs.text) out.push(String(node.attrs.text));
  for (const child of node.children || []) texts(child, out);
  return out;
}
function all(node, pred, out = []) {
  if (!node) return out;
  if (pred(node)) out.push(node);
  for (const child of node.children || []) all(child, pred, out);
  return out;
}

assert.equal(sandbox.companySearchText("Société Générale"), "societe generale");
assert.equal(sandbox.companySearchText("  ACME.CA  "), "acme.ca");

const empty = sandbox.companyMonthSeries([], "2026-10");
assert.equal(empty.length, 12);
assert.equal(empty[0].month, "2025-11");
assert.equal(empty[11].month, "2026-10");
assert.ok(empty.every((month) => month.messages === 0 && month.sent === 0 && month.received === 0));

const sparse = sandbox.companyMonthSeries([
  { month: "2025-11", messages: 1, sent: 0, received: 1 },
  { month: "2026-10", messages: 5, sent: 2, received: 3 },
  { month: "not-a-month", messages: 99, sent: 99, received: 99 },
], "2026-10");
assert.equal(sparse.length, 12);
assert.equal(JSON.stringify(sparse[0]), JSON.stringify({ month: "2025-11", messages: 1, sent: 0, received: 1 }));
assert.equal(sparse[1].messages, 0);
assert.equal(JSON.stringify(sparse[11]), JSON.stringify({ month: "2026-10", messages: 5, sent: 2, received: 3 }));

assert.equal(sandbox.threadStateKey("bogus"), "unknown");
assert.equal(sandbox.threadStateLabel("awaiting_owner"), "Waiting on you");
assert.equal(sandbox.threadStateLabel("bogus"), "Check the thread");
assert.equal(sandbox.companyThreadRank("awaiting_owner"), 0);
assert.equal(sandbox.companyThreadRank("no_action"), 4);
assert.equal(sandbox.companyThreadRank("bogus"), 5);
assert.equal(sandbox.companyThreadReasonLabel("inbound_last"), "Their message is last");
assert.equal(sandbox.companyThreadReasonLabel("owner_last"), "You replied last");
assert.equal(sandbox.companyThreadReasonLabel("not-a-reason"), "");
assert.equal(sandbox.companyThreadReasonLabel("awaiting_owner"), "");

const row = {
  key: "c:atelier nord", label: "Atelier Nord", declared: true, domains: ["atelier-nord.test"],
  people: 3, messages: 5, sent: 2, received: 3, threads: 5, last_at: "2026-10-20T09:00:00Z",
};
const listRow = sandbox.companyListRow(row, true, () => {});
assert.equal(listRow.tag, "button");
assert.equal(listRow.attrs["aria-current"], "true");
assert.ok(String(listRow.attrs.class).includes("selected"));
const listText = texts(listRow).join(" | ");
assert.ok(listText.includes("Atelier Nord"));
assert.ok(listText.includes("atelier-nord.test"));
assert.ok(listText.includes("3 people"));
assert.ok(listText.includes("5 messages"));
assert.ok(!listText.includes("c:atelier nord"));
const inboundOnly = sandbox.companyListRow(
  { ...row, sent: 0, received: 2 }, false, () => {});
assert.ok(texts(inboundOnly).join(" | ").includes("Inbound only"));

const profile = {
  generated_at: "2026-10-26T12:00:00Z",
  key: "c:atelier nord", label: "Atelier Nord", declared: true,
  domains: ["atelier-nord.example", "atelier-nord.test"],
  people: [{
    address: "alice@atelier-nord.test", name: "Alice Martin", department: "Engineering",
    job_title: "CTO", phone: "+1 555 010 0100", declared: true,
    messages: 2, sent: 1, received: 1, last_at: "2026-10-20T09:00:00Z",
  }],
  messages: 5, sent: 2, received: 3, threads: 5,
  first_at: "2026-09-10T09:00:00Z", last_at: "2026-10-20T09:00:00Z",
  last_inbound_at: "2026-10-20T09:00:00Z", last_outbound_at: "2026-10-18T09:00:00Z",
  last_90_days: { messages: 4, sent: 2, received: 2, threads: 3, active_days: 2 },
  months: [{ month: "2026-10", messages: 4, sent: 2, received: 2 }],
  recent: [{
    message_id: 11, subject: "Atelier Nord renewal", sender_name: "Bob Smith",
    instant: "2026-10-20T09:00:00Z", sent: false, has_attachments: true,
  }],
  recent_total: 5,
  open_threads: [
    { conversation_id: "C-art-b", subject: "Follow-up", last_at: "2026-10-21T09:00:00Z", state: "awaiting_owner", reason: "inbound_last", actor: "Alice Martin", message_id: 2, messages: 4 },
    { conversation_id: "C-art-f", subject: "Questionnaire", last_at: "2026-10-20T09:00:00Z", state: "unknown", reason: "visibility_gap", message_id: 3, messages: 2 },
  ],
  open_threads_total: 2,
  thread_states: {
    awaiting_owner: 1, awaiting_other: 0, answered_by_owner: 0,
    answered_by_other: 0, no_action: 0, unknown: 1,
  },
  events: [{
    subject: "Kickoff", start: "2026-11-03T10:00:00Z", end: "2026-11-03T11:00:00Z",
    location: "Teams", all_day: false, organizer_name: "Alice Martin",
    attendees: [{ address: "alice@atelier-nord.test" }], online_url: "https://meet.example/kickoff",
    is_recurring: true,
  }],
  events_total: 1,
  deadlines: [{ message_id: 3, subject: "Invoice", sender_name: "Alice Martin", due: "2026-11-10T09:00:00Z" }],
  deadlines_total: 1,
  people_contacted: 3, people_known: 4,
};
assert.equal(JSON.stringify(sandbox.companyOpenCounts(profile)),
  JSON.stringify({ owner: 1, other: 0, unknown: 1, total: 2 }));
const next = sandbox.companyNextAction(profile);
assert.equal(next.kind, "reply");
assert.equal(next.thread.subject, "Follow-up");
const profileNode = sandbox.companyProfileNode(profile);
const profileText = texts(profileNode).join("\n");
for (const heading of [
  "Relationship", "Mail volume", "Open threads", "Recent mail", "Addresses",
  "Upcoming meetings", "Due commitments",
]) {
  assert.ok(profileText.includes(heading), "missing section: " + heading);
}
assert.ok(profileText.includes("Next step"), "the next action is missing");
assert.ok(profileText.includes("Reply to “Follow-up”"), "the reply action is missing");
assert.ok(profileText.includes("You owe"), "the owner ledger is missing");
assert.ok(profileText.includes("Waiting on them"), "the waiting ledger is missing");
assert.ok(profileText.includes("Their message is last"), "the thread reason is missing");
assert.ok(profileText.includes("4 messages"), "the thread length is missing");
assert.ok(profileText.includes("1 sent · 1 received"), "the address direction counts are missing");
assert.ok(profileText.includes("Attachment"), "the attachment badge is missing");
assert.ok(profileText.includes("Organized by Alice Martin"), "the meeting organizer is missing");
assert.ok(profileText.includes("Join online"), "the meeting link is missing");
assert.ok(profileText.includes("All time"), "the KPI window is not stated");
assert.ok(profileText.includes("Showing 1 of 4."), "the people truncation is not stated");
for (const stateLabel of [
  "Waiting on you", "Waiting on them", "You replied",
  "A colleague replied", "No reply needed", "Check the thread",
]) {
  assert.ok(profileText.includes(stateLabel), "missing friendly state: " + stateLabel);
}
for (const raw of [
  "awaiting_owner", "awaiting_other", "answered_by_owner",
  "answered_by_other", "no_action", "c:atelier nord",
]) {
  assert.ok(!profileText.includes(raw), "raw value reached the profile text: " + raw);
}
const chart = all(profileNode, (entry) => entry.attrs && entry.attrs.role === "img")[0];
assert.ok(chart, "the volume chart is not an image role");
assert.ok(String(chart.attrs["aria-label"]).includes("12 months"));
const hrefs = all(profileNode, (entry) => entry.tag === "a").map((entry) => entry.attrs.href);
assert.ok(hrefs.includes("#/mail?mode=important&id=11"), "recent message does not open Mail");
assert.ok(hrefs.includes("#/mail?mode=important&id=2"), "open thread does not open its message");
assert.ok(hrefs.includes("#/mail?mode=important&id=3"), "deadline does not open its message");
assert.ok(profileText.includes("3 addresses contacted"));
assert.ok(profileText.includes("4 addresses known"));
assert.ok(profileText.includes("+1 555 010 0100"), "the address-book phone is missing from Addresses");
assert.ok(profileText.includes("Ask about this company"), "the company Ask action is missing");

I.setLanguage({ language: "fr", formatLocale: "fr-FR" });
assert.equal(sandbox.threadStateLabel("awaiting_owner"), "En attente de vous");
assert.equal(sandbox.threadStateLabel("bogus"), "Vérifiez la conversation");
const frText = texts(sandbox.companyProfileNode(profile)).join("\n");
assert.ok(frText.includes("Téléphone"), "the phone column label is not localized");
assert.ok(frText.includes("Conversations ouvertes"));
assert.ok(frText.includes("En attente de vous"));
assert.ok(!frText.includes("awaiting_owner"));

console.log("companies behavior: ok");
`
