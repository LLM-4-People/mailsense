package ui

import (
	"strings"
	"testing"
)

// TestProblemDiagnosticHasOneRenderer: the toast list and the error screen show
// the same English problem fields (stage, subject, message) through one helper.
// They used to build the same details block twice, byte for byte.
func TestProblemDiagnosticHasOneRenderer(t *testing.T) {
	http := readUIModule(t, "11-http.js")
	if got := strings.Count(http, "function problemDiagnostic(problem)"); got != 1 {
		t.Fatalf("11-http.js defines problemDiagnostic %d times, want once", got)
	}
	if !strings.Contains(http, `metaLine([problem.stage, problem.subject, problem.message])`) {
		t.Fatal("problemDiagnostic does not join the three problem fields")
	}

	init := readUIModule(t, "52-init.js")
	if strings.Contains(init, "problem.stage, problem.subject, problem.message") {
		t.Fatal("52-init.js builds its own problem line; call problemDiagnostic")
	}
	if !strings.Contains(init, "problemDiagnostic(problem)") {
		t.Fatal("the error screen does not render through problemDiagnostic")
	}
}
