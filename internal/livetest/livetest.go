// Package livetest is the shared support of the live tests run on the
// Windows machine through the device relay (scripts/*-live.sh): a context
// that ends before the test deadline or when the stop file appears, the
// explicit pick of the mailboxes to read, and the limits of a small
// verification run (Limit, applied before the first call; Budget, the
// test's detail reads over all its runs). Open is the one start of a live
// test. Test support only: no production code imports it.
package livetest

import (
	"context"
	"flag"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/syncer"
)

// Context ends 3 minutes before the test deadline (the deferred cleanup
// still runs) or when the stop file appears (a test-owned stop file; ""
// none). The stop file is polled: it is written by another process.
func Context(t *testing.T, stopFile string) context.Context {
	ctx, cancel := context.WithCancel(t.Context())
	if dl, ok := t.Deadline(); ok {
		ctx, cancel = context.WithDeadline(t.Context(), dl.Add(-3*time.Minute))
	}
	t.Cleanup(cancel)
	if stopFile != "" {
		go func() {
			tick := time.NewTicker(2 * time.Second)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					if _, err := os.Stat(stopFile); err == nil {
						t.Logf("LIVE stop requested")
						cancel()
						return
					}
				}
			}
		}()
	}
	return ctx
}

// Pick picks the mailboxes a live test reads, as a user would: "default"
// (the profile's default store) or a comma list of store keys; nothing
// is ever picked by the app itself. It returns the picked keys.
func Pick(t *testing.T, cfg *config.Config, mailboxes []source.Mailbox, pick string) []string {
	t.Helper()
	var keys []string
	for _, m := range mailboxes {
		if (pick == "default" && m.Default) || hasKey(pick, m.Key) {
			keys = append(keys, m.Key)
		}
	}
	if len(keys) == 0 {
		t.Fatalf("no store matches -live.pick=%q", pick)
	}
	if err := source.Pick(cfg, mailboxes, keys...); err != nil {
		t.Fatal(err)
	}
	t.Logf("LIVE picked %d mailbox(es)", len(keys))
	return keys
}

func hasKey(list, key string) bool {
	for _, k := range strings.Split(list, ",") {
		if strings.TrimSpace(k) == key {
			return true
		}
	}
	return false
}

// The limits of a live run, shared by every live test binary.
var (
	details = flag.Int("live.details", 0, "complete at most this many partial messages in the whole test, newest first, "+
		"over all its sync runs and probes (sync.details.max_per_run; 0: all)")
	rate = flag.Float64("live.rate", 0, "Outlook calls per second at most, user away or active "+
		"(outlook.pace.*calls_per_second; 0: the shipped pace)")
)

// Limit applies -live.details and -live.rate to cfg, through the settings
// a user would change — before anything reads the source, so every call
// of the test runs under them: a short verification run (e.g. the newest
// 1,000 detail reads at 100 calls/s, about 10 items/s) instead of a full
// sync. Unpicked stores are never walked for their counts
// (outlook.discover.count_unpicked off: a whole-profile walk is ≈2,600
// calls, most of them on online mailboxes). It returns the test's detail
// budget, shared by all its sync runs and probes.
func Limit(t *testing.T, cfg *config.Config) *Budget {
	t.Helper()
	if *details < 0 || *rate < 0 {
		t.Fatalf("-live.details (%d) and -live.rate (%g) must not be negative", *details, *rate)
	}
	set(t, cfg, "outlook.discover.count_unpicked", false)
	if *rate > 0 {
		// The active pace may not exceed the away pace: lower it first.
		set(t, cfg, "outlook.pace.active.calls_per_second", min(cfg.Float("outlook.pace.active.calls_per_second"), *rate))
		set(t, cfg, "outlook.pace.calls_per_second", *rate)
	}
	b := &Budget{cfg: cfg, left: *details, capped: *details > 0}
	b.Before(t)
	t.Logf("LIVE limits: details in the whole test %d (0: all), calls per second %g (0: shipped pace), unpicked stores not counted",
		*details, *rate)
	return b
}

func set(t *testing.T, cfg *config.Config, key string, v any) {
	t.Helper()
	if err := cfg.Set(key, v); err != nil {
		t.Fatal(err)
	}
}

// Budget is a live test's detail budget (-live.details): every sync run
// and probe of the test draws from it, so a test of three syncs reads at
// most -live.details details in all, not per run.
type Budget struct {
	cfg    *config.Config
	left   int
	capped bool // false: no budget (a full sync)
}

// Before sets the next sync run's detail cap to what is left
// (sync.details.max_per_run), and turns the detail pass off once nothing
// is (sync.details.enabled).
func (b *Budget) Before(t *testing.T) {
	t.Helper()
	if !b.capped {
		set(t, b.cfg, syncer.SettingDetailsMaxPerRun, 0)
		return
	}
	set(t, b.cfg, syncer.SettingDetailsEnabled, b.left > 0)
	set(t, b.cfg, syncer.SettingDetailsMaxPerRun, max(b.left, 1))
}

// Spent records n detail reads (a run's completed and failed ones).
func (b *Budget) Spent(t *testing.T, n int) {
	t.Helper()
	if b.capped {
		b.left = max(b.left-n, 0)
		b.Before(t)
	}
}

// Take draws up to n reads for a probe and returns how many it may make.
func (b *Budget) Take(t *testing.T, n int) int {
	t.Helper()
	if b.capped {
		n = min(n, b.left)
	}
	b.Spent(t, n)
	return n
}

// Discoverer is what a live test reads the stores through
// (outlook.Connector).
type Discoverer interface {
	Discover(ctx context.Context) ([]mail.Folder, error)
	Mailboxes() []source.Mailbox
}

// Open is the one start of a live test on a source: the limits first
// (Limit), then one Discover — unpicked stores listed, not walked — and
// the explicit pick (Pick). It returns the test's detail budget.
func Open(t *testing.T, ctx context.Context, cfg *config.Config, conn Discoverer, pick string) *Budget {
	t.Helper()
	b := Limit(t, cfg)
	if _, err := conn.Discover(ctx); err != nil {
		t.Fatalf("discover: %v", err)
	}
	Pick(t, cfg, conn.Mailboxes(), pick)
	return b
}
