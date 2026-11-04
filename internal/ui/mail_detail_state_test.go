package ui

import "testing"

// The message detail is decided by the message read alone. A stored triage
// result describes a message whose body may already be gone, so a 404, a 410
// or an empty 2xx GET paints the removed/invalid state instead of a bodyless
// "triage result" stub. The triage read stays supplementary: its failure is
// the inline banner and never hides a message that was read.
func TestMailDetailStateFollowsTheMessageRead(t *testing.T) {
	runUISandbox(t, "mail_detail_state", mailDetailStateHarness, "mail detail state behavior: ok")
}

const mailDetailStateHarness = `"use strict";

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
sandbox.tOr = I.tOr.bind(I);
sandbox.I18N = I;

const app = fs.readFileSync(path.join(dir, "app.js"), "utf8");

function marked(name) {
  const start = app.indexOf("// " + name + ":start");
  const end = app.indexOf("// " + name + ":end");
  assert.ok(start >= 0 && end > start, name + " markers are missing");
  return app.slice(start, end);
}

function sourceFunction(name) {
  let start = app.indexOf("  function " + name + "(");
  if (start < 0) start = app.indexOf("  async function " + name + "(");
  assert.ok(start >= 0, name + " is missing");
  const end = app.indexOf("\n  }", start);
  assert.ok(end > start, name + " has no end");
  return app.slice(start, end + 5);
}

// The real error classification, removed-read predicate and failure copy.
const apiStart = app.indexOf("class ApiError");
const apiEnd = app.indexOf("function problemLabel");
assert.ok(apiStart >= 0 && apiEnd > apiStart, "the ApiError slice is missing");
vm.runInContext(app.slice(apiStart, apiEnd), sandbox, { filename: "app.js#api-failure" });
// The real state components the pane paints.
vm.runInContext(marked("unavailable-state"), sandbox, { filename: "app.js#unavailable-state" });
vm.runInContext(marked("state-block"), sandbox, { filename: "app.js#state-block" });
vm.runInContext(marked("warning-banner"), sandbox, { filename: "app.js#warning-banner" });
// The one message-view validator.
vm.runInContext(marked("message-view"), sandbox, { filename: "app.js#message-view" });

function textNode(value) {
  return { tag: "#text", text: String(value), attrs: {}, children: [] };
}
function makeNode(tag, attrs, children) {
  const node = {
    tag, attrs: {}, children: [], text: "", dataset: {}, className: "",
    isConnected: true, scrollTop: 0,
    addEventListener() {},
    setAttribute(name, value) { this.attrs[name] = String(value); },
    append(...kids) { kids.forEach(add); },
  };
  function add(child) {
    if (child === null || child === undefined || child === false) return;
    if (Array.isArray(child)) { child.forEach(add); return; }
    if (child && typeof child === "object" && child.tag) { node.children.push(child); return; }
    node.children.push(textNode(child));
  }
  for (const [key, value] of Object.entries(attrs || {})) {
    if (value === undefined || value === null || value === false) continue;
    if (key === "dataset") { Object.assign(node.dataset, value || {}); continue; }
    if (key === "class") { node.className = String(value); continue; }
    if (key === "text") { node.text = String(value); }
    node.attrs[key] = value;
  }
  (children || []).forEach(add);
  return node;
}
sandbox.h = (tag, attrs, ...children) => makeNode(tag, attrs, children);
sandbox.text = textNode;
sandbox.clear = (node) => { node.children.length = 0; };
sandbox.fragment = (...children) => makeNode("#fragment", {}, children);
sandbox.icon = () => makeNode("svg", { class: "icon" });
sandbox.button = (label, onClick) => {
  const node = makeNode("button", { class: "button", text: label });
  if (onClick) node.addEventListener("click", onClick);
  return node;
};
sandbox.link = (label) => makeNode("a", { class: "button", text: label });
sandbox.badge = (value, kind) => makeNode("span", { class: "badge " + (kind || ""), text: value });
sandbox.sectionHead = (title) => makeNode("div", { class: "section-head", text: title });
sandbox.messageCardNode = (view, message) => makeNode("article", { class: "mail-card", text: message.subject || "" });
sandbox.threadHostNode = () => makeNode("div", { class: "thread-host" });
sandbox.triageSummaryNode = () => makeNode("div", { class: "triage-summary" });
sandbox.actionBadge = () => null;
sandbox.allMailRoleLabel = (value) => String(value || "");
sandbox.scopeVerdictLabel = (value) => String(value || "");
sandbox.routePill = () => null;
sandbox.previewPill = () => null;
sandbox.gateCertification = () => null;
sandbox.orderedNames = (values) => Object.keys(values || {});
sandbox.triageQuestionNames = (names) => (names || []).join(", ");
sandbox.navigate = () => {};
sandbox.routeHash = () => "#/";
sandbox.formatId = (value) => String(value);
sandbox.formatDate = () => "date";
sandbox.skeletonLine = (width) => makeNode("div", { class: "skeleton " + width });
sandbox.state = { questions: null, askPrefill: "" };

// The real detail node and the real detail state machine.
vm.runInContext(sourceFunction("mailDetailNode"), sandbox, { filename: "app.js#mailDetailNode" });
vm.runInContext([
  sourceFunction("mailDetailPendingNode"),
  sourceFunction("appendMailDetailNode"),
  sourceFunction("mailDetailReadState"),
  sourceFunction("loadMailDetail"),
].join("\n"), sandbox, { filename: "app.js#mail-detail-state" });

const ApiError = vm.runInContext("ApiError", sandbox);
const validMessageView = vm.runInContext("validMessageView", sandbox);
const mailDetailReadState = vm.runInContext("mailDetailReadState", sandbox);
const loadMailDetail = vm.runInContext("loadMailDetail", sandbox);

const cache = new Map();
sandbox.cached = (key) => cache.get(key);
sandbox.messagePath = (id) => "/api/messages/" + String(id);
let messageImpl = async () => null;
let triageImpl = async () => null;
let questionsImpl = async () => ({ questions: [] });
sandbox.loadMessage = (id) => messageImpl(id);
sandbox.loadTriageDetail = (id) => triageImpl(id);
sandbox.loadQuestions = () => questionsImpl();
sandbox.hydrateMailThread = () => {};
sandbox.refreshActiveRoute = () => {};
let errorScreens = 0;
sandbox.errorScreen = () => {
  errorScreens += 1;
  throw new Error("errorScreen must not paint inside the detail pane");
};

function makePanel() {
  return makeNode("div", { class: "mail-detail-body", id: "mail-detail" });
}

async function paint(options = {}) {
  const id = options.id || 101;
  cache.clear();
  if (options.previewMessage !== undefined) cache.set("/api/messages/" + id, options.previewMessage);
  if (options.previewTriage !== undefined) cache.set("/api/triage/" + id, options.previewTriage);
  messageImpl = async () => {
    if (options.messageError) throw options.messageError;
    return options.message;
  };
  triageImpl = async () => {
    if (options.triageError) throw options.triageError;
    return options.triage;
  };
  questionsImpl = async () => ({ questions: [] });
  errorScreens = 0;
  const panel = makePanel();
  await loadMailDetail(id, panel, { mode: "search" });
  return panel;
}

function classOf(node) {
  return String(node.className || (node.attrs && node.attrs.class) || "");
}
function walk(node, visit) {
  if (!node) return;
  visit(node);
  for (const child of node.children || []) walk(child, visit);
}
function byClass(root, name) {
  const out = [];
  walk(root, (node) => { if (classOf(node).split(/\s+/).includes(name)) out.push(node); });
  return out;
}
function allText(root) {
  const out = [];
  walk(root, (node) => {
    if (node.tag === "#text") out.push(node.text);
    else if (node.text) out.push(String(node.text));
  });
  return out.join(" | ");
}
function hasText(root, needle) { return allText(root).includes(needle); }
function hasRetry(root) {
  return byClass(root, "button").some((node) => node.text === sandbox.t("common.try_again"));
}

const unavailableCopy = sandbox.t("detail.unavailable");
const invalidCopy = sandbox.t("error.invalid_response");
const teachCopy = sandbox.t("mail.detail.teach");
const askCopy = sandbox.t("mail.detail.ask");

// The validator accepts exactly a message object on a positive row id.
assert.equal(validMessageView(null), false, "null is a valid message view");
assert.equal(validMessageView({}), false, "an empty object is a valid message view");
assert.equal(validMessageView({ message: {} }), false, "a view without row_id is valid");
assert.equal(validMessageView({ row_id: 101 }), false, "a view without message is valid");
assert.equal(validMessageView({ message: {}, row_id: 0 }), false, "row 0 is a valid message view");
assert.equal(validMessageView({ message: {}, row_id: 101 }), true, "a message on a positive row is not valid");
assert.equal(validMessageView({ message: {}, row_id: "101" }), true, "a numeric-string row id is rejected");

const triage = {
  message_id: 101,
  values: { category: { type: "choice", choice: "finance", confidence: 0.9 } },
  stale: false,
  escalate: false,
};
const validView = {
  row_id: 101,
  verdict: "owner",
  message: { subject: "Synthetic invoice", body: "Synthetic body", conversation_id: "" },
};

async function main() {
// The state machine takes both reads but only the message decides.
for (const [label, error] of [
  ["404", new ApiError(404, { code: "not_found", message: "removed" })],
  ["410", new ApiError(410, { code: "http_410", message: "removed" })],
  ["http_410", new ApiError(0, { code: "http_410", message: "removed" })],
]) {
  assert.equal(mailDetailReadState(
    { status: "rejected", reason: error }, { status: "fulfilled", value: triage }).state,
  "removed", label + " is not classified as removed");
  const panel = await paint({ messageError: error, triage });
  assert.equal(panel.children.length, 1, label + ": the removed detail is not one node");
  assert.equal(byClass(panel, "unavailable").length, 1, label + ": the removed detail does not show exactly one unavailable state");
  assert.equal(byClass(panel, "mail-card").length, 0, label + ": the removed detail painted a message card");
  assert.equal(byClass(panel, "triage-summary").length, 0, label + ": the removed detail painted a triage summary");
  assert.ok(hasText(panel, unavailableCopy), label + ": the removed copy is missing");
  assert.ok(!hasText(panel, teachCopy) && !hasText(panel, askCopy), label + ": the removed detail offers Teach or Ask");
  assert.equal(errorScreens, 0, label + ": the pane painted an error screen");
}

// A failed message read is an inline error with a retry, never a screen.
const failure = new ApiError(500, { code: "internal", message: "synthetic detail failure" });
const failed = await paint({ messageError: failure, triage });
assert.equal(byClass(failed, "mail-card").length, 0, "the failed read painted a message card");
assert.equal(byClass(failed, "unavailable").length, 0, "the failed read claims the message was removed");
assert.equal(byClass(failed, "empty").length, 1, "the failed read is not one inline error block");
assert.ok(hasText(failed, "synthetic detail failure"), "the failed read lost its message");
assert.ok(hasRetry(failed), "the failed read has no retry");
assert.equal(errorScreens, 0, "the failed read painted an error screen");

// A fulfilled read without a message is invalid, whatever the triage says.
for (const [label, value] of [
  ["null", null],
  ["empty object", {}],
  ["message without row", { message: {} }],
  ["row without message", { row_id: 101 }],
]) {
  assert.equal(mailDetailReadState(
    { status: "fulfilled", value }, { status: "fulfilled", value: triage }).state,
  "invalid", label + " is not classified as invalid");
  const panel = await paint({ message: value, triage });
  assert.equal(byClass(panel, "mail-card").length, 0, label + ": the invalid read painted a message card");
  assert.equal(byClass(panel, "triage-summary").length, 0, label + ": the invalid read painted a triage summary");
  assert.equal(byClass(panel, "unavailable").length, 0, label + ": the invalid read claims the message was removed");
  assert.ok(hasText(panel, invalidCopy), label + ": the invalid response copy is missing");
  assert.ok(hasRetry(panel), label + ": the invalid read has no retry");
  assert.equal(errorScreens, 0, label + ": the invalid read painted an error screen");
}

// A message that was read is ready even when its triage read failed; the
// failure is the inline banner and the summary is not invented.
const triageFailure = new ApiError(500, { code: "internal", message: "synthetic triage failure" });
assert.equal(mailDetailReadState(
  { status: "fulfilled", value: validView },
  { status: "rejected", reason: triageFailure }).state, "ready");
const ready = await paint({ message: validView, triageError: triageFailure });
assert.equal(byClass(ready, "mail-card").length, 1, "the read message did not paint its card");
assert.equal(byClass(ready, "thread-host").length, 1, "the read message has no thread section");
assert.equal(byClass(ready, "triage-summary").length, 0, "a failed triage read still painted a summary");
assert.equal(byClass(ready, "warning-banner").length, 1, "a failed triage read has no inline banner");
assert.ok(hasText(ready, "synthetic triage failure"), "the triage banner lost its message");
assert.ok(hasText(ready, teachCopy) && hasText(ready, askCopy), "the read message has no Teach or Ask action");

// The card, then the thread, then the summary.
const normal = await paint({ message: validView, triage });
const content = byClass(normal, "mail-detail-content")[0];
assert.ok(content, "the ready detail has no content host");
const layout = content.children.map((node) => classOf(node));
const cardAt = layout.findIndex((name) => name.includes("mail-card"));
const threadAt = layout.findIndex((name) => name.includes("thread-host"));
const summaryAt = layout.findIndex((name) => name.includes("detail-answers"));
assert.ok(cardAt >= 0 && threadAt > cardAt && summaryAt > threadAt,
  "the ready detail order is card=" + cardAt + " thread=" + threadAt + " summary=" + summaryAt);

// A cached triage alone is not a detail: the message read is still pending,
// and only its arrival paints the card.
cache.clear();
cache.set("/api/triage/101", triage);
let resolveMessage;
messageImpl = () => new Promise((resolve) => { resolveMessage = resolve; });
triageImpl = async () => triage;
questionsImpl = async () => ({ questions: [] });
errorScreens = 0;
const pendingPanel = makePanel();
const pending = loadMailDetail(101, pendingPanel, { mode: "search" });
assert.equal(byClass(pendingPanel, "mail-card").length, 0, "a cached triage painted a message card");
assert.equal(byClass(pendingPanel, "triage-summary").length, 0, "a cached triage painted a triage summary");
assert.equal(byClass(pendingPanel, "unavailable").length, 0, "a cached triage painted a removed state");
assert.equal(byClass(pendingPanel, "mail-detail-skeleton").length, 1, "a cached triage did not leave the message pending");
resolveMessage(validView);
await pending;
assert.equal(byClass(pendingPanel, "mail-card").length, 1, "the message read did not replace the pending detail");
assert.equal(byClass(pendingPanel, "triage-summary").length, 1, "the ready detail lost its triage summary");

console.log("mail detail state behavior: ok");
}

main().catch((error) => {
  console.error(error);
  process.exit(1);
});
`
