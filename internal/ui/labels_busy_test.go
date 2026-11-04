package ui

import (
	"strings"
	"testing"
)

// localFunctionBody returns a function body up to the close brace at its own
// indentation. A nested block closes deeper, so it cannot cut the body short.
func localFunctionBody(t *testing.T, js, signature string) string {
	t.Helper()
	start := strings.Index(js, signature)
	if start < 0 {
		t.Fatalf("%q not found", signature)
	}
	lineStart := strings.LastIndex(js[:start], "\n") + 1
	indent := js[lineStart:start]
	end := strings.Index(js[start:], "\n"+indent+"}")
	if end <= 0 {
		t.Fatalf("the end of %q was not found", signature)
	}
	return js[start : start+end]
}

// A label save has one task-level busy owner. Save, Accept, Skip, Previous and
// Next all go through it, and every entry point that replaces the mounted task
// checks the saving flag before it acts. Only the post-save advance and the
// queue's own mount may call showLabelTask directly.
func TestLabelTaskActionsShareOneBusyGuard(t *testing.T) {
	js := readUIModule(t, "26-labels.js")

	busy := localFunctionBody(t, js, "function setTaskBusy(on) {")
	for _, want := range []string{
		"taskActions.set(on)",
	} {
		if !strings.Contains(busy, want) {
			t.Errorf("setTaskBusy lacks %q", want)
		}
	}
	owner := localFunctionBody(t, js, "function labelTaskActions(bar) {")
	for _, want := range []string{
		`bar.setAttribute("aria-busy", busy ? "true" : "false")`,
		"const disabled = busy || entry.blocked();",
		"entry.node.disabled = disabled",
		`entry.node.setAttribute("aria-disabled"`,
		`entry.node.classList.toggle("is-disabled", disabled)`,
	} {
		if !strings.Contains(owner, want) {
			t.Errorf("the action-bar busy owner lacks %q", want)
		}
	}

	// The five task-changing actions are registered with their availability
	// outside a save so clearing it restores exactly those controls.
	for _, want := range []string{
		"{ node: saveButton, blocked: () => !hasPrimaryAnswer() }",
		"{ node: useSuggestionButton, blocked: () => false }",
		"{ node: skipButton, blocked: () => false }",
		"{ node: prevButton, blocked: () => index === 0 }",
		"{ node: nextButton, blocked: () => index + 1 >= tasks.length }",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("the task action bar does not register %q", want)
		}
	}

	// Navigation has two guarded owners. A new entry point that calls
	// showLabelTask directly would bypass the save guard and fail here.
	for _, signature := range []string{
		"function goToTask(index) {",
		"function skipTask(taskIndex) {",
		"async function saveTask(trigger, override) {",
	} {
		body := localFunctionBody(t, js, signature)
		if !strings.Contains(body, "if (saving) return;") {
			t.Errorf("%s does not check the saving flag", signature)
		}
	}
	for _, want := range []string{
		`button(t("labels.task.prev"), () => goToTask(index - 1)`,
		`button(t("labels.task.next"), () => goToTask(index + 1)`,
		`const skipButton = button(t("labels.form.skip"), () => skipTask(index)`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("a task entry point does not use the busy guard: %q", want)
		}
	}
	keys := localFunctionBody(t, js, "function handleKey(event) {")
	for _, want := range []string{
		"saveTask(saveButton);",
		"saveTask(useSuggestionButton, suggestion);",
		"skipTask(index);",
	} {
		if !strings.Contains(keys, want) {
			t.Errorf("a keyboard entry point bypasses the busy guard: %q", want)
		}
	}

	save := localFunctionBody(t, js, "async function saveTask(trigger, override) {")
	for _, want := range []string{
		"setTaskBusy(true);",
		"setTaskBusy(false);",
		"const savedId = task.message_id;",
		"const savedAt = tasks.findIndex((item) => item.message_id === savedId);",
		"if (savedAt >= 0) tasks.splice(savedAt, 1);",
		"const current = tasks.findIndex((item) => item.message_id === savedId);",
		"showLabelTask(current, { focusHeading: true });",
	} {
		if !strings.Contains(save, want) {
			t.Errorf("saveTask lacks %q", want)
		}
	}
	if strings.Contains(save, "tasks.splice(index, 1)") {
		t.Error("saveTask still splices the captured index instead of the task identity")
	}
}
