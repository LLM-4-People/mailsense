package main

import (
	"os"
	"strings"
	"testing"
)

// TestTriageMaintenanceHasNoSecondBatchDefault: the catch-up path takes the
// automatic batch from triage.AutoBatch (the schema's value). A literal fallback
// here used to disagree with the schema default (100 vs 500).
func TestTriageMaintenanceHasNoSecondBatchDefault(t *testing.T) {
	data, err := os.ReadFile("triagemaintenance.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	if !strings.Contains(source, "triage.AutoBatch(a.cfg)") {
		t.Fatal("triagemaintenance.go does not resolve the batch through triage.AutoBatch")
	}
	for _, banned := range []string{"batch = 100", "SettingBatch"} {
		if strings.Contains(source, banned) {
			t.Fatalf("triagemaintenance.go keeps %q; the batch is triage.AutoBatch's", banned)
		}
	}
}
