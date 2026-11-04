package triage

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
)

// TestAutoBatchReadsTheSchemaDefault: the automatic batch is the schema's
// value, not a second number in code. The trigger and the catch-up path resolve
// it through the same accessor.
func TestAutoBatchReadsTheSchemaDefault(t *testing.T) {
	cfg := configtest.New(t, nil)
	def, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	want := def.Int(SettingBatch)
	if want < 1 {
		t.Fatalf("the schema default for %s is %d, want at least 1", SettingBatch, want)
	}
	if got := AutoBatch(cfg); got != want {
		t.Fatalf("AutoBatch = %d, want the schema default %d", got, want)
	}

	w := newWorld(t, nil)
	trig := &syncTrigger{t: w.tr}
	o, ok := trig.options()
	if !ok {
		t.Fatal("automatic triage is off with the default settings")
	}
	if o.Limit != AutoBatch(w.cfg) {
		t.Fatalf("the trigger runs %d, the accessor resolves %d", o.Limit, AutoBatch(w.cfg))
	}
}
