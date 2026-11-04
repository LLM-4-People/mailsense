package config_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
)

func TestOverlayMergesTheUsersFile(t *testing.T) {
	cfg := configtest.New(t, nil)
	shipped := []byte(`{"_doc": "x", "rules": {"a": {"v": 1}, "b": {"v": 2}}, "n": 1}`)
	setting := "evidence.rules.file"
	if got, err := cfg.Overlay(setting, shipped, "rules"); err != nil || string(got) != string(shipped) {
		t.Fatalf("no file: the shipped data unchanged: %s %v", got, err)
	}
	path := cfg.Paths().Resolve(cfg.String(setting))
	if err := os.WriteFile(path, []byte(`{"rules": {"a": null, "c": {"v": 3}}, "n": 2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := cfg.Overlay(setting, shipped, "rules")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Rules map[string]map[string]int `json:"rules"`
		N     int                       `json:"n"`
	}
	if err := json.Unmarshal(got, &doc); err != nil || doc.N != 2 || doc.Rules["a"] != nil || doc.Rules["b"]["v"] != 2 || doc.Rules["c"]["v"] != 3 {
		t.Fatalf("%s %v", got, err)
	}
	for name, data := range map[string]string{"not an object": `[1]`, "junk": `{`} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := cfg.Overlay(setting, shipped, "rules"); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := cfg.Overlay(setting, []byte(`[]`)); err == nil {
		t.Error("shipped data that is not an object accepted")
	}
}
