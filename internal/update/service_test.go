package update

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
)

var serviceTestTime = time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)

// updateHarness is one service over a fake GitHub and a manual clock. The
// default answer is a newer release; each test changes the handler or the
// clock and observes the service, never the wall clock or the network.
type updateHarness struct {
	svc    *Service
	cfg    *config.Config
	clk    *clocktest.Fake
	github *fakeGitHub
}

func newUpdateHarness(t *testing.T, current, latestTag string) *updateHarness {
	t.Helper()
	clk := clocktest.New(serviceTestTime)
	github, _ := newFakeGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		jsonOK(w, releaseJSON(latestTag, "The release notes"))
	})
	github.setListHandler(func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, `[{"tag_name":"v0.2.0"},{"tag_name":"v0.1.5"},{"tag_name":"v0.1.0"}]`)
	})
	cfg := configtest.New(t, map[string]any{
		SettingCheckInterval: "24h",
		SettingRemindAfter:   "1h",
	})
	svc, err := New(Options{
		Config:  cfg,
		Clock:   clk,
		Client:  &http.Client{Timeout: 5 * time.Second},
		BaseURL: github.url,
		Current: current,
		Rand:    func() float64 { return 1 },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &updateHarness{svc: svc, cfg: cfg, clk: clk, github: github}
}

func mustCheck(t *testing.T, h *updateHarness) Status {
	t.Helper()
	status, err := h.svc.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	return status
}

func mustMaybeCheck(t *testing.T, h *updateHarness) Status {
	t.Helper()
	status, err := h.svc.MaybeCheck(context.Background())
	if err != nil {
		t.Fatalf("MaybeCheck: %v", err)
	}
	return status
}

// TestStatusMakesNoNetworkCall is the request-path contract: reading the
// update status is local and never spends the GitHub rate limit.
func TestStatusMakesNoNetworkCall(t *testing.T) {
	h := newUpdateHarness(t, "0.1.0", "v0.2.0")
	for range 3 {
		status := h.svc.Status()
		if status.Available || status.Latest != "" || status.CheckedAt != "" {
			t.Fatalf("an untouched status = %+v", status)
		}
	}
	if h.github.count() != 0 || h.github.listCount() != 0 {
		t.Fatalf("Status made %d/%d requests", h.github.count(), h.github.listCount())
	}
}

// TestMaybeCheckHonoursIntervalAndJitter pins the persisted gate: a check runs
// at the interval plus up to 10% jitter, not on every wake-up, and the setting
// turns the automatic path off entirely.
func TestMaybeCheckHonoursIntervalAndJitter(t *testing.T) {
	t.Run("jitter delays the due check", func(t *testing.T) {
		h := newUpdateHarness(t, "0.1.0", "v0.2.0")
		first := mustMaybeCheck(t, h)
		if !first.Available || h.github.count() != 1 || h.github.listCount() != 1 {
			t.Fatalf("first check = %+v, requests %d/%d", first, h.github.count(), h.github.listCount())
		}
		// The default interval is 24h; the harness jitter source returns 1,
		// so the next automatic check is due after 26h24m.
		h.clk.Advance(24*time.Hour + 2*time.Hour + 24*time.Minute - time.Second)
		mustMaybeCheck(t, h)
		if h.github.count() != 1 {
			t.Fatalf("a check before interval+jitter ran (%d requests)", h.github.count())
		}
		h.clk.Advance(time.Second)
		second := mustMaybeCheck(t, h)
		if !second.Available || h.github.count() != 2 {
			t.Fatalf("a due check did not run: %+v, %d requests", second, h.github.count())
		}
		// The disabled setting wins over a due interval.
		if err := h.cfg.Set(SettingCheckEnabled, false); err != nil {
			t.Fatal(err)
		}
		h.clk.Advance(48 * time.Hour)
		mustMaybeCheck(t, h)
		if h.github.count() != 2 {
			t.Fatalf("a disabled automatic check made a request (%d)", h.github.count())
		}
	})

	t.Run("no jitter uses the bare interval", func(t *testing.T) {
		h := newUpdateHarness(t, "0.1.0", "v0.2.0")
		h.svc.rand = func() float64 { return 0 }
		mustMaybeCheck(t, h)
		h.clk.Advance(24*time.Hour - time.Second)
		mustMaybeCheck(t, h)
		if h.github.count() != 1 {
			t.Fatalf("a check before the bare interval ran (%d requests)", h.github.count())
		}
		h.clk.Advance(time.Second)
		mustMaybeCheck(t, h)
		if h.github.count() != 2 {
			t.Fatalf("a check at the bare interval did not run (%d requests)", h.github.count())
		}
	})
}

// TestCheckIsSingleFlight proves a startup pass and a manual check cannot
// issue two concurrent requests; the second caller reads the current status.
func TestCheckIsSingleFlight(t *testing.T) {
	h := newUpdateHarness(t, "0.1.0", "v0.2.0")
	entered := make(chan struct{})
	release := make(chan struct{})
	h.github.setHandler(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		jsonOK(w, releaseJSON("v0.2.0", "notes"))
	})

	firstErr := make(chan error, 1)
	go func() {
		_, err := h.svc.Check(context.Background())
		firstErr <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the first check never reached the endpoint")
	}

	type answer struct {
		status Status
		err    error
	}
	secondDone := make(chan answer, 1)
	go func() {
		status, err := h.svc.Check(context.Background())
		secondDone <- answer{status: status, err: err}
	}()
	select {
	case got := <-secondDone:
		if got.err != nil {
			t.Fatalf("concurrent Check: %v", got.err)
		}
		if got.status.CheckedAt != "" || h.github.count() != 1 {
			t.Fatalf("concurrent Check = %+v, requests %d", got.status, h.github.count())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a concurrent Check blocked instead of reading the current status")
	}

	close(release)
	select {
	case err := <-firstErr:
		if err != nil {
			t.Fatalf("first Check: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the first check did not finish")
	}
	if h.github.count() != 1 {
		t.Fatalf("single-flight issued %d requests", h.github.count())
	}
}

// TestSkipAndMarkSeen covers the What's New markers of the one state file.
func TestSkipAndMarkSeen(t *testing.T) {
	h := newUpdateHarness(t, "0.1.0", "v0.2.0")
	status := mustCheck(t, h)
	if !status.Available || status.Skipped || status.NotesSeen {
		t.Fatalf("fresh status = %+v", status)
	}
	// A marker for another version does not mute the current offer.
	if _, err := h.svc.Skip("0.1.0"); err != nil {
		t.Fatal(err)
	}
	if again := h.svc.Status(); again.Skipped {
		t.Fatalf("a skip of another version muted the offer: %+v", again)
	}
	skipped, err := h.svc.Skip("0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if !skipped.Skipped {
		t.Fatalf("Skip did not mute the offer: %+v", skipped)
	}
	// At exactly update.remind.after the offer returns.
	h.clk.Advance(time.Hour)
	if again := h.svc.Status(); again.Skipped {
		t.Fatalf("the remind window did not expire: %+v", again)
	}
	seen, err := h.svc.MarkSeen("0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if !seen.NotesSeen {
		t.Fatalf("MarkSeen did not record the marker: %+v", seen)
	}
	for _, operation := range []func(string) (Status, error){h.svc.Skip, h.svc.MarkSeen} {
		if _, err := operation("  "); err == nil {
			t.Fatal("an empty version was accepted")
		}
	}
}

// TestAvailableOnlyWhenLatestIsNewer is the release comparison contract: a
// parsed latest must order after the running build. Equal, older, malformed
// and an unparseable running build all stay unavailable.
func TestAvailableOnlyWhenLatestIsNewer(t *testing.T) {
	cases := []struct {
		name          string
		current       string
		latest        string
		available     bool
		latestVersion string
		behind        int
	}{
		{"newer patch", "0.1.0", "v0.2.0", true, "0.2.0", 1},
		{"equal", "0.2.0", "v0.2.0", false, "0.2.0", 0},
		{"older", "0.3.0", "v0.2.0", false, "0.2.0", 0},
		{"prerelease of the running version", "0.2.0", "v0.2.0-rc.1", false, "0.2.0-rc.1", 0},
		{"malformed latest", "0.1.0", "nightly", false, "nightly", 0},
		{"unparseable current", "dev", "v0.2.0", false, "0.2.0", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newUpdateHarness(t, tc.current, tc.latest)
			status := mustCheck(t, h)
			if status.Available != tc.available || status.Latest != tc.latestVersion {
				t.Fatalf("status = %+v, want available=%v latest=%q", status, tc.available, tc.latestVersion)
			}
			if status.VersionsBehind != tc.behind {
				t.Fatalf("versions_behind = %d, want %d", status.VersionsBehind, tc.behind)
			}
			if tc.available {
				if h.github.listCount() != 1 {
					t.Fatalf("a newer release did not fetch the one release-list page (%d)", h.github.listCount())
				}
				if status.ReleaseURL == "" || status.Notes != "The release notes" {
					t.Fatalf("release fields = %+v", status)
				}
			} else if h.github.listCount() != 0 {
				t.Fatalf("an unavailable release spent %d release-list requests", h.github.listCount())
			}
		})
	}
}

// TestFailedCheckRecordsQuietState pins the quiet failure: the service returns
// no error, records a stable code and message, and never turns a failed check
// into a healthy "up to date" status. A later success clears the error.
func TestFailedCheckRecordsQuietState(t *testing.T) {
	cases := []struct {
		name    string
		handler func(w http.ResponseWriter, r *http.Request)
		code    string
	}{
		{"server error", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}, ErrorHTTP},
		{"rate limited", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)
			jsonOK(w, `{"message":"slow down"}`)
		}, ErrorRateLimited},
		{"invalid answer", func(w http.ResponseWriter, r *http.Request) {
			jsonOK(w, `{"tag_name":`)
		}, ErrorInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newUpdateHarness(t, "0.1.0", "v0.2.0")
			h.github.setHandler(tc.handler)
			status, err := h.svc.Check(context.Background())
			if err != nil {
				t.Fatalf("a failed endpoint answer must stay quiet: %v", err)
			}
			if status.ErrorCode != tc.code || status.LastError == "" || status.CheckedAt == "" {
				t.Fatalf("failed status = %+v, want code %q", status, tc.code)
			}
			if status.Available || status.Latest != "" {
				t.Fatalf("a failed check claimed a release: %+v", status)
			}
			// The next successful answer clears the quiet error and detects.
			h.github.setHandler(func(w http.ResponseWriter, r *http.Request) {
				jsonOK(w, releaseJSON("v0.2.0", "notes"))
			})
			recovered := mustCheck(t, h)
			if recovered.ErrorCode != "" || recovered.LastError != "" || !recovered.Available {
				t.Fatalf("recovered status = %+v", recovered)
			}
		})
	}
}
