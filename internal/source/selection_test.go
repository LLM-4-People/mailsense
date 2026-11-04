package source_test

import (
	"errors"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// Review L0 rules §7: the selection settings refuse malformed entries,
// Scope names a selection once whatever the order or duplicates, and the
// actions refuse keys no discovery listed.
func TestSelectionSettingsAndScope(t *testing.T) {
	k1, k2 := source.StoreKey("s", "1"), source.StoreKey("s", "2")
	cfg := configtest.New(t, map[string]any{source.SettingSelected: []any{k2, k1, k2}})
	if got := source.Scope(cfg.Snapshot()); got != k1+","+k2 && got != k2+","+k1 || len(source.Selected(cfg.Snapshot())) != 2 {
		t.Fatalf("scope %q", got)
	}
	if source.Scope(cfg.Snapshot()) != source.Scope(configtest.New(t, map[string]any{source.SettingSelected: []any{k1, k2}}).Snapshot()) {
		t.Fatal("the same selection is the same scope")
	}
	for name, v := range map[string]any{
		"declared without =":   []any{k1 + "amelie@example.org"},
		"declared, empty addr": []any{k1 + "= "},
		"declared, bad key":    []any{"primary=amelie@example.org"},
	} {
		if err := cfg.Set(source.SettingDeclared, v); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	if err := cfg.Set(source.SettingSelected, []any{"primary"}); err == nil {
		t.Error("a key that is not a store key must be refused")
	}
	known := []source.Mailbox{{Key: k1, SourceID: "s", StoreID: "1", Kind: mail.StorePrimary}}
	if err := source.Pick(cfg, known, k1, "store-000000000000"); !errors.Is(err, source.ErrUnknownMailbox) {
		t.Fatalf("an unknown key fails the whole pick: %v", err)
	}
	if err := source.Declare(cfg, known, k1, "李娜@例子.中国"); err != nil {
		t.Fatal(err)
	}
	if got := source.Declared(cfg.Snapshot())[k1]; len(got) != 1 || got[0] != "李娜@例子.中国" {
		t.Fatalf("declared as given (the identity normalizes): %v", got)
	}
}
