package priority

import (
	"os"
	"strings"
	"testing"
)

// TestRankedViewHelpersHaveOneOwner: the three ranked views share one count
// check, one panic-to-error conversion and one paused-refresh record. A second
// spelling is how one view starts reporting a different bound or dropping the
// panic's type.
func TestRankedViewHelpersHaveOneOwner(t *testing.T) {
	body, err := os.ReadFile("priority.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	helpers, err := os.ReadFile("view_errors.go")
	if err != nil {
		t.Fatal(err)
	}
	owner := string(helpers)
	if got := strings.Count(owner+source, "count must be between 1 and"); got != 1 {
		t.Errorf("the count bound is spelled %d times, want once in countError", got)
	}
	if got := strings.Count(owner+source, "panicked (%T)"); got != 1 {
		t.Errorf("the panic error is spelled %d times, want once in panicError", got)
	}
	if got := strings.Count(owner+source, "ranking refresh paused by the resource guard"); got != 1 {
		t.Errorf("the paused log is spelled %d times, want once", got)
	}
	for _, call := range []string{`countError("top", n)`, `countError("action", limit)`, `countError("decision", limit)`} {
		if !strings.Contains(source, call) {
			t.Errorf("priority.go lacks %q", call)
		}
	}

	if got, want := countError("top", 0).Error(), "priority: top count must be between 1 and 200, got 0"; got != want {
		t.Errorf("countError = %q, want %q", got, want)
	}
	if got, want := countError("action", 201).Error(), "priority: action count must be between 1 and 200, got 201"; got != want {
		t.Errorf("countError = %q, want %q", got, want)
	}
}
