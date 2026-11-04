//go:build windows && embedlive

package search

// Live verification of the semantic stack on the Windows target. It uses
// the app's real runtime directory
// (%LOCALAPPDATA%\Mailsense\runtime): artifacts are downloaded ON THE
// MACHINE when missing. With sidecar.variant = auto and a fresh re-probe
// it downloads, verifies and starts the platform's one managed build
// (Vulkan on Windows; its canary uses the model's pinned vector) and
// chooses it, starts a second manager (which must not probe again),
// measures throughput on the newest -live.messages primary-store Inbox messages
// read through the Outlook connector into a temporary database (under the
// live limits: -live.details, -live.rate; the private live wrapper ran the
// Outlook crash guard around it), indexes
// them, measures search latency, and checks that the runtime dies with
// its app (job object). Read-only on Outlook. The report holds aggregates
// only; the temporary database is removed.
//
//	embed-live.exe -test.run TestEmbedLive -test.v -test.timeout 10800s -live.report=r.json -live.stop=job.stop
//	    -live.details=1000 -live.rate=100

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/fsx"
	"github.com/LLM-4-People/mailsense/internal/index"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/livetest"
	"github.com/LLM-4-People/mailsense/internal/sidecar"
	"github.com/LLM-4-People/mailsense/internal/store"
)

var (
	liveReport   = flag.String("live.report", "embed-live-report.json", "aggregate report path")
	liveStop     = flag.String("live.stop", "", "stop (cancel, clean up) when this file appears")
	liveMessages = flag.Int("live.messages", 300, "newest primary Inbox messages to measure on")
)

const (
	liveRuntime = "llama-server"
	liveModel   = "embeddinggemma-2"
)

type liveData struct {
	Started     string                    `json:"started"`
	Finished    string                    `json:"finished"`
	RuntimeDir  string                    `json:"runtime_dir"`
	GPUBefore   []string                  `json:"gpu_before"` // name, total, used, free MiB, driver
	SyncSeconds float64                   `json:"sync_seconds"`
	SyncedInbox int                       `json:"synced_inbox"`
	Sample      int                       `json:"sample"`
	First       startReport               `json:"first_start"`  // auto, after a re-probe
	Second      startReport               `json:"second_start"` // must not probe again
	Choices     map[string]sidecar.Choice `json:"choices"`
	Tuning      map[string]sidecar.Tuning `json:"tuning"`
	Served      throughput                `json:"served"` // the chosen build on the sample
	Index       *index.Report             `json:"index"`
	Search      map[string]any            `json:"search"`
	DiesWithApp string                    `json:"dies_with_app"`
	Builds      []sidecar.BuildInfo       `json:"builds"`
}

func liveCtx(t *testing.T) context.Context { return livetest.Context(t, *liveStop) }

// liveConfig is a temporary data directory whose runtime directory is
// the app's real one.
func liveConfig(t *testing.T, name string, bus *events.Bus) (*config.Config, string) {
	dir, _ := filepath.Abs(name)
	home, _ := os.UserHomeDir()
	app, err := config.ResolvePaths(os.Environ(), "windows", home)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(config.Options{Paths: config.Paths{DataDir: dir}, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fsx.RemoveAll(dir, cfg.Snapshot().Replace(nil)); err != nil {
			t.Errorf("LIVE could not remove the temporary data directory: %v", err)
		}
	})
	must(t, cfg.Set("sidecar.dir", filepath.Join(app.DataDir, "runtime")))
	return cfg, filepath.Join(app.DataDir, "runtime")
}

func TestEmbedLive(t *testing.T) {
	ctx := liveCtx(t)
	bus := events.New()
	cfg, runtimeDir := liveConfig(t, "embed-live-data", bus)
	rep := liveData{Started: time.Now().UTC().Format(time.RFC3339), RuntimeDir: runtimeDir, Search: map[string]any{}}
	st, err := store.Open(ctx, cfg, bus, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rep.GPUBefore = gpu()
	t.Logf("LIVE gpu before: %v", rep.GPUBefore)
	texts := sampleInbox(t, ctx, cfg, bus, st, &rep)
	jm := jobs.NewManager(cfg, bus, nil)
	rep.First = startAuto(t, ctx, cfg, bus, jm, texts, true, &rep.Served)
	rep.Second = startAuto(t, ctx, cfg, bus, jm, texts, false, nil)
	if rep.Second.Probed || rep.Second.Variant != rep.First.Variant {
		t.Errorf("LIVE the second start probed again or chose differently: %+v vs %+v", rep.Second, rep.First)
	}
	indexAndSearch(t, ctx, cfg, jm, st, &rep)
	rep.DiesWithApp = diesWithApp(t, cfg.Paths().DataDir)
	if !strings.HasPrefix(rep.DiesWithApp, "yes") {
		t.Errorf("LIVE job object check: %s", rep.DiesWithApp)
	}
	m := newManager(t, cfg, nil, jm)
	rep.Tuning, rep.Choices, _ = m.Learned()
	rep.Builds, _ = m.Builds()
	m.Stop()
	rep.Finished = time.Now().UTC().Format(time.RFC3339)
	b, _ := json.MarshalIndent(rep, "", "  ")
	must(t, fsx.WriteFileAtomic(*liveReport, b, fsx.FilePerm, cfg.Snapshot().Replace(nil)))
	t.Logf("LIVE chosen=%s probed_first=%v probed_second=%v dies_with_app=%s report=%s", rep.First.Variant, rep.First.Probed, rep.Second.Probed, rep.DiesWithApp, *liveReport)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
