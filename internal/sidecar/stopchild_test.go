package sidecar

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/logx"
)

// recordingEnder observes the grace and clock the instance passes to a child's
// one stop.
type recordingEnder struct {
	calls int
	grace time.Duration
	clk   clock.Clock
}

func (r *recordingEnder) end(_ *child, grace time.Duration, clk clock.Clock) {
	r.calls++
	r.grace, r.clk = grace, clk
}

// TestStopChildUsesTheOneConfiguredGrace: every probe, canary, tune and restart
// path stops a child with the same configured grace on the manager's clock.
func TestStopChildUsesTheOneConfiguredGrace(t *testing.T) {
	cfg := configtest.New(t, map[string]any{"sidecar.stop_timeout": "1234ms"})
	clk := clocktest.New(time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC))
	m := &Manager{cfg: cfg, clock: clk, log: logx.For("test"), inst: map[string]*instance{}}
	in := newInstance(m, "llama-server", "model")

	done := make(chan struct{})
	close(done)
	rec := &recordingEnder{}
	in.stopChild(&child{end: rec, done: done})

	if rec.calls != 1 {
		t.Fatalf("the child was ended %d time(s), want 1", rec.calls)
	}
	if rec.grace != 1234*time.Millisecond {
		t.Fatalf("grace = %v, want the configured 1234ms", rec.grace)
	}
	if rec.clk != clk {
		t.Fatal("the stop did not use the manager's clock")
	}
}

// TestStopGraceIsSpelledOnce: the child stops go through stopChild; the launch
// path's WaitDelay and the supervisor join read the setting for different uses
// and stay visible as such.
func TestStopGraceIsSpelledOnce(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	stops := 0
	perFile := map[string]int{}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		source := string(data)
		stops += strings.Count(source, "in.stopChild(")
		if file != "instance.go" && strings.Contains(source, ".stop(in.m.cfg.Duration(") {
			t.Fatalf("%s stops a child with the setting inline; use in.stopChild", file)
		}
		if n := strings.Count(source, `"sidecar.stop_timeout"`); n > 0 {
			perFile[file] = n
		}
	}
	if stops != 6 {
		t.Fatalf("%d child stops go through stopChild, want 6", stops)
	}
	// instance.go owns the helper; start.go reads the same value for WaitDelay
	// and manager.go for the supervisor join.
	helper, err := os.ReadFile("instance.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(helper), "func (in *instance) stopChild(c *child) {") {
		t.Fatal("instance.go no longer owns stopChild")
	}
	if perFile["instance.go"] != 1 || perFile["start.go"] != 1 || perFile["manager.go"] != 1 {
		t.Fatalf("the stop-timeout reads are %v, want instance.go:1 start.go:1 manager.go:1", perFile)
	}
}
