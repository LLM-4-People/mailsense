package logx

import (
	"bufio"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/config/configtest"
	"github.com/LLM-4-People/mailsense/internal/events"
)

func setup(t *testing.T, bus *events.Bus, overrides map[string]any) (*Service, *config.Config) {
	t.Helper()
	cfg := configtest.NewWithBus(t, bus, overrides)
	svc, err := Setup(cfg, bus)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc, cfg
}

func last(t *testing.T, svc *Service) Entry {
	t.Helper()
	all := svc.Ring().Snapshot()
	if len(all) == 0 {
		t.Fatal("ring is empty")
	}
	return all[len(all)-1]
}

func TestEntryReachesRingFileAndEvents(t *testing.T) {
	bus := events.New()
	sub, _ := events.Subscribe(bus, TopicEntry, 16)
	svc, cfg := setup(t, bus, nil)
	For("store").Info("opened", "path", "/x.db", "rows", 3, slog.Group("g", "inner", "v"))
	e := last(t, svc)
	if e.Component != "store" || e.Message != "opened" || e.Attrs["rows"] != int64(3) || e.Attrs["g.inner"] != "v" {
		t.Fatalf("entry: %+v", e)
	}
	select {
	case got := <-sub.C:
		if got.Seq != e.Seq {
			t.Fatal("event differs from ring entry")
		}
	default: // Publish is synchronous: the event must already be buffered
		t.Fatal("no event published")
	}
	_ = svc.Close()
	f, err := os.Open(filepath.Join(cfg.Paths().DataDir, "logs", "mailsense.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var lines int
	for sc.Scan() {
		var parsed Entry
		if err := json.Unmarshal(sc.Bytes(), &parsed); err != nil {
			t.Fatalf("invalid JSON line %q: %v", sc.Text(), err)
		}
		lines++
	}
	if lines == 0 {
		t.Fatal("log file is empty")
	}
}

func TestComponentLevelsAndInheritance(t *testing.T) {
	svc, _ := setup(t, nil, map[string]any{"log.level": "warn", "log.components": []string{"source=debug", "source.outlook=error"}})
	before := len(svc.Ring().Snapshot())
	For("store").Info("hidden")
	For("source.archive").Debug("shown")
	For("source.outlook").Warn("hidden")
	For("source.outlook.com").Error("shown")
	got := svc.Ring().Snapshot()[before:]
	if len(got) != 2 || got[0].Message != "shown" || got[1].Component != "source.outlook.com" {
		t.Fatalf("levels: %+v", got)
	}
}

func TestRedaction(t *testing.T) {
	svc, _ := setup(t, nil, nil)
	RegisterSecret("hunter2-super-secret")
	RegisterSecret("abc") // shorter than log.redact.min_secret_length: not replaced
	RegisterSecret("")
	log := For("t")
	log.Info("key hunter2-super-secret leaked", "auth", "Bearer abcdefghijklmnop", "err", errors.New("token=xyz123 failed"),
		"obj", struct{ K string }{"hunter2-super-secret"}, "short", "abc", "sk", "sk-ABCDEFGHIJKLMNOPQRST")
	e := last(t, svc)
	raw, _ := json.Marshal(e)
	for _, leak := range []string{"hunter2", "abcdefghijklmnop", "xyz123", "sk-ABCDEF"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("%q leaked: %s", leak, raw)
		}
	}
	if e.Attrs["short"] != "abc" {
		t.Error("short secret must not be replaced")
	}
}

func TestOddValuesStayValidJSON(t *testing.T) {
	svc, _ := setup(t, nil, nil)
	For("t").Info("odd", "nan", math.NaN(), "inf", math.Inf(1), "d", 1500*time.Millisecond, "ts", time.Unix(0, 0), "", "empty-key", "nil", nil)
	e := last(t, svc)
	if _, err := e.line(); err != nil {
		t.Fatal(err)
	}
	if e.Attrs["d"] != "1.5s" || e.Attrs["ts"] != "1970-01-01T00:00:00Z" {
		t.Fatalf("attrs: %+v", e.Attrs)
	}
}

func TestLiveReconfiguration(t *testing.T) {
	bus := events.New()
	applied, _ := events.Subscribe(bus, TopicApplied, 8)
	svc, cfg := setup(t, bus, nil)
	<-applied.C // Setup
	if err := cfg.Set("log.level", "error"); err != nil {
		t.Fatal(err)
	}
	<-applied.C
	if err := cfg.Set("log.ring.size", 10); err != nil {
		t.Fatal(err)
	}
	<-applied.C
	if svc.Ring().Cap() != 10 || For("t").Enabled(t.Context(), slog.LevelWarn) {
		t.Fatal("configuration change not applied")
	}
	for range 30 {
		For("t").Error("x")
	}
	if n := len(svc.Ring().Snapshot()); n != 10 {
		t.Fatalf("ring holds %d, want 10", n)
	}
}

func TestHandlerAttrsAndGroups(t *testing.T) {
	svc, _ := setup(t, nil, nil)
	log := For("t").With("a", 1).WithGroup("g").With("b", 2).WithGroup("").WithGroup("h")
	log.Info("m", "c", 3)
	e := last(t, svc)
	if e.Attrs["a"] != int64(1) || e.Attrs["g.b"] != int64(2) || e.Attrs["g.h.c"] != int64(3) {
		t.Fatalf("attrs %+v", e.Attrs)
	}
}

func TestFileFailureHealthTransitions(t *testing.T) {
	bus := events.New()
	healthSub, _ := events.Subscribe(bus, TopicHealth, 8)
	svc, cfg := setup(t, bus, nil)
	logDir := filepath.Join(cfg.Paths().DataDir, "logs")
	// Make the active file unwritable: replace it with a directory after closing it.
	_ = svc.file.Close()
	_ = os.Remove(filepath.Join(logDir, "mailsense.log"))
	_ = os.Mkdir(filepath.Join(logDir, "mailsense.log"), 0o700)
	For("t").Error("lost")
	For("t").Error("lost again")
	h := <-healthSub.C
	if !h.FileFailing || h.FileErrors != 1 || h.LastFileError == "" {
		t.Fatalf("first failure: %+v", h)
	}
	if got := svc.Health(); got.FileErrors != 2 || !got.FileFailing {
		t.Fatalf("health %+v", got)
	}
	_ = os.Remove(filepath.Join(logDir, "mailsense.log"))
	For("t").Error("back")
	if h = <-healthSub.C; h.FileFailing {
		t.Fatalf("recovery must be published: %+v", h)
	}
	if len(healthSub.C) != 0 {
		t.Fatal("only transitions are published")
	}
}

func TestCloseRestoresDefaultPipeline(t *testing.T) {
	svc, _ := setup(t, nil, nil)
	_ = svc.Close()
	if c := active.Load(); c.ring != nil || c.file != nil || c.levels.def != bootstrapLevel {
		t.Fatal("default pipeline not restored")
	}
}

func TestSetupFailsOnUnwritableLogDir(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(blocker, nil, 0o600)
	cfg := configtest.New(t, map[string]any{"log.file.dir": blocker})
	if _, err := Setup(cfg, nil); err == nil {
		t.Fatal("expected an error when the log dir is a file")
	}
}
