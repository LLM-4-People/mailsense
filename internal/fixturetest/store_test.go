package fixturetest

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail/mailtest"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// TestOpenStorePicksAndRegistersTheStandardSource: the helper's contract is
// one registered source with its mailbox-1 store picked, so seeders can use
// the standard message shape without repeating the boot block.
func TestOpenStorePicksAndRegistersTheStandardSource(t *testing.T) {
	cfg, st := OpenStore(t, nil)
	standard := mailtest.Message(SourceID, "picked")
	want := source.StoreKey(standard.SourceID, standard.StoreID)
	if got := cfg.Strings(source.SettingSelected); len(got) != 1 || got[0] != want {
		t.Fatalf("picked stores %v, want %q", got, want)
	}
	sources, err := st.ListSources(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[0].ID != SourceID {
		t.Fatalf("registered sources %+v", sources)
	}
}
