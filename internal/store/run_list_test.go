package store

import (
	"os"
	"strings"
	"testing"
)

// TestRunListersShareOneQuery: the five run histories are one read. A lister
// that builds its own SELECT is how one table starts ordering differently or
// forgetting the limit clamp.
func TestRunListersShareOneQuery(t *testing.T) {
	for _, c := range []struct{ file, fn string }{
		{"lists.go", "func (s *Store) ListRuns("},
		{"embeddings.go", "func (s *Store) ListIndexRuns("},
		{"evidence.go", "func (s *Store) ListEvidenceRuns("},
		{"layer1.go", "func (s *Store) ListLayer1Runs("},
		{"triage.go", "func (s *Store) ListTriageRuns("},
	} {
		data, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatal(err)
		}
		source := string(data)
		start := strings.Index(source, c.fn)
		if start < 0 {
			t.Fatalf("%s: %s not found", c.file, c.fn)
		}
		end := strings.Index(source[start:], "\n}")
		if end < 0 {
			t.Fatalf("%s: %s has no body end", c.file, c.fn)
		}
		body := source[start : start+end]
		if !strings.Contains(body, "listRuns(ctx, s,") {
			t.Errorf("%s: %s does not read through listRuns", c.file, c.fn)
		}
		if strings.Contains(body, "ORDER BY") {
			t.Errorf("%s: %s builds its own run query", c.file, c.fn)
		}
	}
	data, err := os.ReadFile("lists.go")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "func listRuns["); got != 1 {
		t.Errorf("lists.go defines listRuns %d times, want once", got)
	}
}

// TestRunListClampsItsLimit: the shared read treats a non-positive limit as
// one row, so a caller cannot ask for "no limit" by accident.
func TestRunListClampsItsLimit(t *testing.T) {
	s, _ := open(t, nil)
	ctx := t.Context()
	for i := 0; i < 2; i++ {
		if _, err := s.BeginIndexRun(ctx, "space"); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.ListIndexRuns(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("ListIndexRuns(0) returned %d rows, want the one-row clamp", len(rows))
	}
	rows, err = s.ListIndexRuns(ctx, -5)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("ListIndexRuns(-5) returned %d rows, want the one-row clamp", len(rows))
	}
}
