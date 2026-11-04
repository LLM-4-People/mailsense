package ui

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// The triage page carries answers, not the question catalogue: shipping the
// whole profile duplicated ~16 KB per request and drifted from
// /api/questions. The screen reads the shared cached catalogue.
func TestTriageReadsTheSharedQuestionCatalogue(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	// Word boundaries: Companies has a view.profiles map, not a profile.
	pageProfile := regexp.MustCompile(`\b(?:page|view)\.profile\b`)
	if pageProfile.MatchString(js) {
		t.Fatal("the triage screen still reads a profile from the page")
	}
	start := strings.Index(js, "async function renderRecent(")
	if start < 0 {
		t.Fatal("renderRecent not found")
	}
	end := strings.Index(js[start:], "function loadMoreTriage(")
	if end < 0 {
		end = strings.Index(js[start:], "async function renderRecentMore(")
	}
	if end < 0 {
		end = 4000
		if end > len(js)-start {
			end = len(js) - start
		}
	}
	block := js[start : start+end]
	if !strings.Contains(block, "loadQuestions()") {
		t.Fatal("the triage screen does not load the shared question catalogue")
	}
	if !strings.Contains(block, "const profile = state.questions || {};") {
		t.Fatal("the triage screen does not render from the shared catalogue")
	}
	if !strings.Contains(js, "triageListRow(id, view.results[id], state.questions || {}") {
		t.Fatal("the triage list still passes a page-owned profile")
	}
}
