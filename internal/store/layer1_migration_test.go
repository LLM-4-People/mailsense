package store

import (
	"strings"
	"testing"
)

// The layer-1 state CHECK is a historical schema copy of the Go vocabulary.
// The migration is never edited in place, so this test fails when a state is
// added or renamed without a new migration that rebuilds the table.
func TestLayer1StatesMatchTheirMigration(t *testing.T) {
	raw, err := migrationFS.ReadFile("migrations/0011_layer1.sql")
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	start := strings.Index(body, "CHECK (state IN (")
	if start < 0 {
		t.Fatal("the layer-1 migration no longer checks its state")
	}
	rest := body[start+len("CHECK (state IN ("):]
	end := strings.Index(rest, "))")
	if end < 0 {
		t.Fatal("the layer-1 state CHECK is malformed")
	}
	sqlStates := map[string]bool{}
	for _, part := range strings.Split(rest[:end], ",") {
		sqlStates[strings.Trim(strings.TrimSpace(part), "'")] = true
	}
	goStates := []string{Layer1Active, Layer1Candidate, Layer1Retired}
	if len(sqlStates) != len(goStates) {
		t.Fatalf("migration accepts %d states, the Go vocabulary has %d", len(sqlStates), len(goStates))
	}
	for _, state := range goStates {
		if !sqlStates[state] {
			t.Fatalf("migration rejects the %q state", state)
		}
	}
}
