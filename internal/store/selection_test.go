package store

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// Review L0 bugs #6 (failed first: the unpicked store's mail stayed in
// search, the vector filter, scope and EachMessage until its source ran
// again): the selection applies at the store's choke point at once, and a
// re-pick brings the kept mail back.
func TestUnpickTakesTheStoresMailOutAtOnce(t *testing.T) {
	keep, drop := source.StoreKey("src", "store-1"), source.StoreKey("src", "store-2")
	s, cfg := open(t, map[string]any{source.SettingSelected: []any{keep, drop}})
	ctx := t.Context()
	a := mailtest.Message("src", "a")
	b := mailtest.Message("src", "b")
	b.StoreID = "store-2"
	if err := s.UpsertMessages(ctx, 0, []mail.Message{a, b}); err != nil {
		t.Fatal(err)
	}
	visible := func() (ids, hits, each int, stores []StoreRef) {
		all, err := s.FilterIDs(ctx, SearchFilter{})
		if err != nil {
			t.Fatal(err)
		}
		res, _ := s.Search(ctx, "budget", 10)
		_ = s.EachMessage(ctx, MessageFilter{}, func(*MessageRow) error { each++; return nil })
		stores, _ = s.ScopeStores(ctx)
		return len(all), res.Total, each, stores
	}
	if ids, hits, each, stores := visible(); ids != 2 || hits != 2 || each != 2 || len(stores) != 2 {
		t.Fatalf("both picked: %d %d %d %+v", ids, hits, each, stores)
	}
	gen := s.Generation()
	if err := cfg.Set(source.SettingSelected, []any{keep}); err != nil {
		t.Fatal(err)
	}
	if s.Generation() == gen {
		t.Fatal("a new selection is a new generation (cached views are not reused)")
	}
	if ids, hits, each, stores := visible(); ids != 1 || hits != 1 || each != 1 || len(stores) != 1 || stores[0].StoreID != "store-1" {
		t.Fatalf("after unpick: %d %d %d %+v", ids, hits, each, stores)
	}
	if byRole, _ := s.CountByRole(ctx); byRole[mail.RoleInbox] != 2 {
		t.Fatalf("the unpicked store's mail is kept: %v", byRole)
	}
	if err := cfg.Set(source.SettingSelected, []any{keep, drop}); err != nil {
		t.Fatal(err)
	}
	if ids, _, _, _ := visible(); ids != 2 {
		t.Fatalf("re-picked: %d", ids)
	}
	// Nothing picked: nothing visible, nothing deleted.
	if err := cfg.Set(source.SettingSelected, []any{}); err != nil {
		t.Fatal(err)
	}
	if ids, hits, each, stores := visible(); ids != 0 || hits != 0 || each != 0 || len(stores) != 0 {
		t.Fatalf("nothing picked: %d %d %d %+v", ids, hits, each, stores)
	}
}

// A selection change that updates no folder row (a picked store with no
// folders, or a contact-only store) still changes every derived view's input.
// The generation must move on the scope change, not on rows affected.
func TestSelectionChangeWithoutFolderRowsMovesMailGeneration(t *testing.T) {
	s, cfg := open(t, nil)
	first := s.MailGeneration()
	if err := cfg.Set(source.SettingSelected, []any{source.StoreKey("src", "store-x")}); err != nil {
		t.Fatal(err)
	}
	if got := s.MailGeneration(); got == first {
		t.Fatalf("a selection change with no folder rows left the generation at %d", first)
	}
}
