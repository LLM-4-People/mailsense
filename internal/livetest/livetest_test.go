package livetest

import (
	"context"
	"flag"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
)

// fakeSource records the settings in force when it is first read.
type fakeSource struct {
	cfg  *config.Config
	seen map[string]any
}

func (f *fakeSource) Discover(context.Context) ([]mail.Folder, error) {
	if f.seen == nil {
		f.seen = map[string]any{}
		for _, k := range []string{"outlook.discover.count_unpicked", "outlook.pace.calls_per_second",
			"outlook.pace.active.calls_per_second", "sync.details.max_per_run"} {
			f.seen[k], _ = f.cfg.Snapshot().Value(k)
		}
	}
	return nil, nil
}

func (f *fakeSource) Mailboxes() []source.Mailbox {
	return []source.Mailbox{{Key: "store-000000000001", SourceID: "outlook", StoreID: "S1", Default: true, Kind: mail.StorePrimary}}
}

func flags(t *testing.T, d, r string) {
	t.Helper()
	old := map[string]string{"live.details": flag.Lookup("live.details").Value.String(), "live.rate": flag.Lookup("live.rate").Value.String()}
	for k, v := range map[string]string{"live.details": d, "live.rate": r} {
		if err := flag.Set(k, v); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for k, v := range old {
			_ = flag.Set(k, v)
		}
	})
}

// Review of the crash-safety round, #1: the live tests discovered the
// whole profile (every unpicked store walked for its counts: 7 stores,
// 589 folders, ≈2,570 calls) BEFORE the limits applied. Open applies them
// first: the first Discover already runs without counting unpicked
// stores, at the -live.rate pace, under the detail cap.
func TestOpenAppliesTheLimitsBeforeTheFirstRead(t *testing.T) {
	flags(t, "1000", "100")
	cfg := configtest.New(t, nil)
	src := &fakeSource{cfg: cfg}
	Open(t, t.Context(), cfg, src, "default")
	want := map[string]any{"outlook.discover.count_unpicked": false, "outlook.pace.calls_per_second": 100.0,
		"outlook.pace.active.calls_per_second": 20.0, "sync.details.max_per_run": int64(1000)}
	for k, v := range want {
		if got := src.seen[k]; got != v {
			t.Errorf("at the first Discover %s = %v (%T), want %v", k, got, got, v)
		}
	}
	if got := cfg.Strings(source.SettingSelected); len(got) != 1 {
		t.Fatalf("picked %v", got)
	}
}

// #6: the per-run cap gave a three-sync test three times the budget (and
// the unread probe more). The budget is the test's: each run draws from
// it, and once it is spent the detail pass is off.
func TestTheDetailBudgetIsTheWholeTests(t *testing.T) {
	flags(t, "1000", "0")
	cfg := configtest.New(t, nil)
	b := Limit(t, cfg)
	if cfg.Int("sync.details.max_per_run") != 1000 || !cfg.Bool("sync.details.enabled") {
		t.Fatal("first run: the whole budget")
	}
	b.Spent(t, 990) // the full sync read 990
	if cfg.Int("sync.details.max_per_run") != 10 {
		t.Fatalf("second run's cap %d, want what is left", cfg.Int("sync.details.max_per_run"))
	}
	if n := b.Take(t, 25); n != 10 {
		t.Fatalf("a probe of 25 gets %d, want the 10 left", n)
	}
	if cfg.Bool("sync.details.enabled") {
		t.Fatal("budget spent: the detail pass must be off")
	}
	if n := b.Take(t, 25); n != 0 {
		t.Fatalf("nothing left: %d", n)
	}
	// No budget (0: a full sync, the user's decision): no cap, never off.
	flags(t, "0", "0")
	cfg2 := configtest.New(t, nil)
	b2 := Limit(t, cfg2)
	b2.Spent(t, 50000)
	if cfg2.Int("sync.details.max_per_run") != 0 || !cfg2.Bool("sync.details.enabled") || b2.Take(t, 25) != 25 {
		t.Fatal("a full sync has no budget")
	}
	// The rate is honoured below the active pace too.
	flags(t, "0", "5")
	cfg3 := configtest.New(t, nil)
	Limit(t, cfg3)
	if cfg3.Float("outlook.pace.calls_per_second") != 5 || cfg3.Float("outlook.pace.active.calls_per_second") != 5 {
		t.Fatalf("rate 5: %v / %v", cfg3.Float("outlook.pace.calls_per_second"), cfg3.Float("outlook.pace.active.calls_per_second"))
	}
}
