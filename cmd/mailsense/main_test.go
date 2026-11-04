package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/bootstrap"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/learning"
)

type reports struct {
	dir string
	err error
}

func (r *reports) report(dir string, err error) string { r.dir, r.err = dir, err; return "" }

type previewLatch struct{ scope string }

func (l *previewLatch) LearnedScope(context.Context) (string, error) { return l.scope, nil }
func (l *previewLatch) SetLearnedScope(_ context.Context, scope string) error {
	l.scope = scope
	return nil
}

type previewStage struct{}

func (previewStage) ID() string { return "test" }
func (previewStage) Status(context.Context) (learning.StageStatus, error) {
	return learning.StageStatus{State: learning.Blocked}, nil
}

// The production adapter reads the machine's one effective preview rule and
// its real Open for the gate answer, so the raw setting alone can never make
// the API, the hub or the topbar claim an open gate or a preview.
func TestLearningAPIReportsTheRealGateAndEffectivePreview(t *testing.T) {
	cfg := configtest.New(t, nil)
	m, err := learning.New(func() string { return "store-111111111111" }, &previewLatch{}, nil, nil, previewStage{})
	if err != nil {
		t.Fatal(err)
	}
	m.SetPreview(func() bool { return cfg.Bool("learning.test_mode") })
	apiLearning := learningAPI{&wire{app: &app{cfg: cfg, learning: m}}}
	ctx := t.Context()

	status, gate, err := apiLearning.Status(ctx)
	if err != nil || gate.Open || status.State != learning.Stuck {
		t.Fatalf("closed: state=%q gate=%+v err=%v", status.State, gate, err)
	}
	if gate.Preview {
		t.Fatal("preview must be off by default")
	}
	if err := cfg.Set("learning.test_mode", true); err != nil {
		t.Fatal(err)
	}
	status, gate, err = apiLearning.Status(ctx)
	if err != nil || !gate.Preview {
		t.Fatalf("preview does not follow the effective rule: %+v err=%v", gate, err)
	}
	if gate.Open {
		t.Fatal("the preview must not open the real gate")
	}
	if err := m.Allow(ctx); err != nil {
		t.Fatalf("preview must unlock Allow: %v", err)
	}
	status, gate, err = apiLearning.Status(ctx)
	if err != nil || gate.Open || status.State != learning.Stuck {
		t.Fatalf("preview changed the real gate: state=%q gate=%+v err=%v", status.State, gate, err)
	}
	if err := cfg.Set("learning.test_mode", false); err != nil {
		t.Fatal(err)
	}
	_, gate, err = apiLearning.Status(ctx)
	if err != nil || gate.Preview {
		t.Fatalf("preview must be reversible: %+v err=%v", gate, err)
	}
	if err := m.Allow(ctx); !errors.Is(err, learning.ErrLocked) {
		t.Fatalf("preview off must restore the locked gate: %v", err)
	}
}

func TestRunStartsAndClearsAStaleReport(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, bootstrap.ErrorFile), []byte("old"), 0o600)
	var r reports
	shutdown := newShutdownRequest()
	if err := shutdown.Request(t.Context()); err != nil {
		t.Fatal(err)
	}
	if code := runWithShutdown([]string{"MAILSENSE_DATA_DIR=" + dir}, runtime.GOOS, "", r.report, shutdown); code != 0 || r.err != nil {
		t.Fatalf("exit %d, report %v", code, r.err)
	}
	if _, err := os.Stat(filepath.Join(dir, "mailsense.db")); err != nil {
		t.Fatal("store not created")
	}
	if _, err := os.Stat(filepath.Join(dir, bootstrap.ErrorFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a stale startup report must be removed after a good start")
	}
	if _, err := os.Stat(filepath.Join(dir, "runtime", "api.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the runtime descriptor must be removed on shutdown")
	}
}

func TestRunReportsEveryStartupFailure(t *testing.T) {
	dir := t.TempDir()
	cases := map[string][]string{
		"invalid configuration":            {"MAILSENSE_DATA_DIR=" + dir, "MAILSENSE_LOG_LEVEL=loud"},
		"cannot locate the data directory": {},
	}
	for want, env := range cases {
		var r reports
		if code := run(env, "windows", "", r.report); code != 1 || r.err == nil || !strings.Contains(r.err.Error(), want) {
			t.Errorf("%s: exit %d, report %v", want, code, r.err)
		}
	}
	// A bad settings file must name the file to fix and say the mail data is
	// safe: the owner otherwise has no next step.
	var cfgReport reports
	badDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(badDir, "settings.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"MAILSENSE_DATA_DIR=" + badDir}, runtime.GOOS, "", cfgReport.report); code != 1 || cfgReport.err == nil {
		t.Fatalf("invalid configuration: exit %d, report %v", code, cfgReport.err)
	}
	for _, want := range []string{"settings.json", "Fix or rename", "mail data is untouched"} {
		if !strings.Contains(cfgReport.err.Error(), want) {
			t.Errorf("settings report %q does not name %q", cfgReport.err, want)
		}
	}
	blocked := t.TempDir()
	_ = os.Mkdir(filepath.Join(blocked, "mailsense.db"), 0o700) // the database path is a directory
	var r reports
	if code := run([]string{"MAILSENSE_DATA_DIR=" + blocked}, runtime.GOOS, "", r.report); code != 1 || r.dir != blocked ||
		!strings.Contains(r.err.Error(), "database") {
		t.Fatalf("store failure: exit %d, report %v in %q", code, r.err, r.dir)
	}
}

// An unconfigured provider set (missing file, empty selector or a selector
// naming nothing) must not stop startup: the owner configures providers
// from the running app, and every action reports an explicit blocker.
func TestRunStartsWithNoProvidersConfigured(t *testing.T) {
	for name, env := range map[string][]string{
		"empty":              nil,
		"unknown selector":   {"MAILSENSE_TRIAGE_DECIDER=nope", "MAILSENSE_JUDGE_PROVIDER=nope", "MAILSENSE_QA_PROVIDER=nope", "MAILSENSE_EMBED_PROVIDER=nope"},
		"invalid dimensions": {"MAILSENSE_EMBED_DIMENSIONS=300"},
	} {
		dir := t.TempDir()
		shutdown := newShutdownRequest()
		if err := shutdown.Request(context.Background()); err != nil {
			t.Fatal(err)
		}
		var r reports
		args := append([]string{"MAILSENSE_DATA_DIR=" + dir}, env...)
		if code := runWithShutdown(args, runtime.GOOS, "", r.report, shutdown); code != 0 || r.err != nil {
			t.Errorf("%s: exit %d, report %v", name, code, r.err)
		}
	}
}

func TestSingleInstance(t *testing.T) {
	dir := t.TempDir()
	env := []string{"MAILSENSE_DATA_DIR=" + dir}
	firstShutdown := newShutdownRequest()
	var first reports
	done := make(chan int, 1)
	go func() {
		done <- runWithShutdown(env, runtime.GOOS, "", first.report, firstShutdown)
	}()

	// Wait for the descriptor the API writes after binding, which is after
	// the first instance took the data-directory lock. Probing the lock
	// itself would race: the probe can win it and make the first instance
	// exit as if a second one were already running.
	descriptor := filepath.Join(dir, "runtime", "api.json")
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(descriptor); err == nil {
			break
		}
		select {
		case code := <-done:
			t.Fatalf("the first instance exited before becoming ready: %d (%v)", code, first.err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the first instance never became ready")
		}
		time.Sleep(10 * time.Millisecond)
	}

	var second reports
	if code := runWithShutdown(env, runtime.GOOS, "", second.report, newShutdownRequest()); code != 1 ||
		second.err == nil || !strings.Contains(second.err.Error(), "another Mailsense instance") {
		t.Fatalf("second instance: exit %d, report %v", code, second.err)
	}
	if second.dir != dir {
		t.Fatalf("the report went to %q", second.dir)
	}

	if err := firstShutdown.Request(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 0 || first.err != nil {
			t.Fatalf("first instance: exit %d, report %v", code, first.err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("the first instance did not stop")
	}
	if _, err := os.Stat(filepath.Join(dir, "runtime", "api.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the descriptor survived the first instance")
	}

	// The lock is released, so a later launch of the same data directory works.
	restart := newShutdownRequest()
	if err := restart.Request(context.Background()); err != nil {
		t.Fatal(err)
	}
	var third reports
	if code := runWithShutdown(env, runtime.GOOS, "", third.report, restart); code != 0 || third.err != nil {
		t.Fatalf("restart: exit %d, report %v", code, third.err)
	}
}
