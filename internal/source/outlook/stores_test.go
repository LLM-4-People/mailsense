package outlook

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// Rules review 1b §6.2: the store list and phase measurements are
// published; capabilities and stats copies are what the UI reads.
func TestStoresAndPhasesArePublished(t *testing.T) {
	o := profile()
	bus := events.New()
	phases, _ := events.Subscribe(bus, TopicPhase, 64)
	c, err := newConnector("outlook", configtest.New(t, bothStores(nil)), bus, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	folders, err := c.Discover(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Sync(t.Context(), folderByRole(t, folders, "primary", "inbox"), "", &memSink{}); err != nil {
		t.Fatal(err)
	}
	if list := c.Mailboxes(); len(list) != 2 || list[1].Key != primaryKey {
		t.Fatalf("mailboxes %+v", list)
	}
	seen := map[string]bool{}
	for len(phases.C) > 0 {
		p := <-phases.C
		seen[string(p.Phase)] = true
		if (p.Phase == "snapshot" || p.Phase == "discover-store") && p.StoreKey == "" {
			t.Fatalf("phase events carry the stable store key: %+v", p)
		}
	}
	for _, want := range []string{"discover", "discover-store", "snapshot", "table"} {
		if !seen[want] {
			t.Errorf("phase %s not published: %v", want, seen)
		}
	}
	if caps := c.Capabilities(); !caps.Incremental || !caps.Bodies || !caps.Recipients || !caps.Deletes || caps.LiveEvents ||
		!caps.OwnerIdentity || !caps.AddressedToMe || !caps.FolderRoles || caps.MultiFolder || !caps.Attachments {
		t.Fatalf("capabilities %+v", caps)
	}
	snap := c.Stats()
	snap.Details.Failures["mutated"] = 1
	snap.Discover.Skipped["mutated"] = 1
	if again := c.Stats(); again.Details.Failures["mutated"] != 0 || again.Discover.Skipped["mutated"] != 0 {
		t.Fatal("Stats returns an isolated copy")
	}
}

func TestRecountWalksUnpickedStoresAgain(t *testing.T) {
	o := profile()
	c, _ := testConnector(t, o, map[string]any{source.SettingSelected: []any{primaryKey}, "outlook.discover.count_unpicked": true})
	_, _ = c.Discover(t.Context())
	walks := callsTo(o, "Store.GetRootFolder")
	c.Recount()
	_, _ = c.Discover(t.Context())
	if callsTo(o, "Store.GetRootFolder")-walks != 2 || c.Stores()[0].CountedAt == "" {
		t.Fatal("after Recount the unpicked store is walked (and timestamped) again")
	}
}

func TestHealth(t *testing.T) {
	o := profile()
	c, _ := testConnector(t, o, nil)
	h, err := c.Health(t.Context())
	if err != nil || !h.Connected || h.Version == "" || h.Elevated || h.NewOutlook {
		t.Fatalf("%+v %v", h, err)
	}
	o2 := profile()
	o2.elevated = true
	c2, _ := testConnector(t, o2, nil)
	if h, err := c2.Health(t.Context()); err == nil || h.Connected || !h.Elevated || h.Error == "" {
		t.Fatalf("elevated: %+v %v", h, err)
	}
}
