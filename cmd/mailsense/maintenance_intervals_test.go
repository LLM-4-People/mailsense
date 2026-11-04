package main

import (
	"os"
	"strings"
	"testing"
)

// TestMaintenanceIntervalsHaveNoLocalDefault: the catch-up and maintenance
// intervals are the schema's values, read through their accessors; a literal
// fallback here would drift from the default the settings screen shows.
func TestMaintenanceIntervalsHaveNoLocalDefault(t *testing.T) {
	for _, c := range []struct {
		file string
		want []string
		ban  []string
	}{
		{"triagemaintenance.go", []string{"triage.AutoInterval(a.cfg)"}, []string{"triageCatchUpDefault", "5 * time.Minute"}},
		{"storemaintenance.go", []string{"a.cfg.Duration(store.SettingMaintainEvery)"}, []string{"5 * time.Minute"}},
	} {
		data, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatal(err)
		}
		source := string(data)
		for _, want := range c.want {
			if !strings.Contains(source, want) {
				t.Errorf("%s lacks %q", c.file, want)
			}
		}
		for _, ban := range c.ban {
			if strings.Contains(source, ban) {
				t.Errorf("%s keeps %q; the schema owns the default", c.file, ban)
			}
		}
	}
}
