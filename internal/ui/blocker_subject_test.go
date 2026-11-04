package ui

import (
	"strings"
	"testing"
)

// TestBlockerSubjectHasOneOwner: the label of a blocker's question is decided
// once. Before this rule the card translated a known question and the Home
// rail painted the raw name, so the same blocker read two ways on one screen.
func TestBlockerSubjectHasOneOwner(t *testing.T) {
	core := readUIModule(t, "00-core.js")
	if got := strings.Count(core, "function blockerSubjectText(blocker)"); got != 1 {
		t.Fatalf("00-core.js defines blockerSubjectText %d times, want once", got)
	}
	start := strings.Index(core, "function blockerNode(entry)")
	end := strings.Index(core, "function sectionHead(")
	if start < 0 || end <= start {
		t.Fatal("blockerNode not found")
	}
	card := core[start:end]
	if !strings.Contains(card, "blockerSubjectText(blocker)") {
		t.Error("blockerNode does not read the shared subject label")
	}
	if strings.Contains(card, "questionTitle(blocker.subject)") {
		t.Error("blockerNode still resolves the subject itself")
	}

	hub := readUIModule(t, "20-hub.js")
	rail := functionBody(t, hub, "function hubRailNodes(view)")
	if !strings.Contains(rail, "blockerSubjectText(blocker)") {
		t.Error("the Hub rail paints its own blocker subject; use blockerSubjectText")
	}
	if strings.Contains(rail, "text: blocker.subject") {
		t.Error("the Hub rail still paints the raw question name")
	}
}

// TestBlockerSubjectTextResolvesKnownQuestions runs the one helper with a known
// question (the friendly title), an unknown one (the raw name) and an absent
// subject (nothing).
func TestBlockerSubjectTextResolvesKnownQuestions(t *testing.T) {
	runUISandbox(t, "blocker_subject", blockerSubjectHarness, "blocker subject behavior: ok")
}

const blockerSubjectHarness = `"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const dir = process.env.UI_DIR;
if (!dir) throw new Error("UI_DIR is required");

const app = fs.readFileSync(path.join(dir, "app.js"), "utf8");
const start = app.indexOf("function blockerSubjectText(");
if (start < 0) throw new Error("blockerSubjectText missing");
const end = app.indexOf("\n  }\n", start) + 4;

const sandbox = {
  questionHelp: (name) => (name === "category" ? { help: "category" } : null),
  questionTitle: (name) => ({ category: "What is this about?" }[name] || name),
};
sandbox.window = sandbox;
vm.createContext(sandbox);
vm.runInContext(app.slice(start, end) + "\nthis.blockerSubjectText = blockerSubjectText;", sandbox, { filename: "app.js#blocker-subject" });

const subject = sandbox.blockerSubjectText;
assert.equal(subject({ subject: "category" }), "What is this about?");
assert.equal(subject({ subject: "untitled_question" }), "untitled_question");
assert.equal(subject({ subject: "" }), "");
assert.equal(subject({}), "");
assert.equal(subject(null), "");
console.log("blocker subject behavior: ok");
`
