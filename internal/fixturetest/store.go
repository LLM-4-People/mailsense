package fixturetest

import (
	"maps"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// SourceID is the synthetic source id every test store registers.
const SourceID = "src"

// OpenStore opens a migrated temp store with the standard picked source
// (SourceID's mailbox store that mailtest.Message uses) and registers it.
// overrides replace the default mailboxes.selected and add any other setting
// the test needs.
func OpenStore(t testing.TB, overrides map[string]any) (*config.Config, *store.Store) {
	t.Helper()
	settings := map[string]any{
		source.SettingSelected: mailtest.Picked([]string{SourceID}),
	}
	maps.Copy(settings, overrides)
	cfg := configtest.New(t, settings)
	st, err := store.Open(t.Context(), cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.UpsertSource(t.Context(), SourceID, "test", "Test", source.Capabilities{}); err != nil {
		t.Fatal(err)
	}
	return cfg, st
}
