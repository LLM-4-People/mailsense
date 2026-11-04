package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/events"
)

func load(t *testing.T, settings string, environ ...string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	if settings != "" {
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return Load(Options{Paths: Paths{DataDir: dir}, Environ: environ})
}

func TestEmbeddedSchemaIsValidAndDefaultsLoad(t *testing.T) {
	s, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := load(t, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range s.Settings() {
		if st.Description == "" || st.Group == "" {
			t.Errorf("%s: description and group are required", st.Key)
		}
		if cfg.Snapshot().Layer(st.Key) != LayerDefault {
			t.Errorf("%s: expected default layer", st.Key)
		}
	}
	if cfg.String("log.level") != "info" || cfg.Int("log.file.max_files") != 5 ||
		cfg.Duration("store.busy_timeout") != 5*time.Second || cfg.Float("sync.skip_ratio_fatal") != 0.01 ||
		cfg.Bool("log.console.enabled") || len(cfg.Ints("http.retry.statuses")) != 4 ||
		cfg.Int("qa.answer_language.min_letters") != 30 || cfg.Int("index.runs.keep") != 100 ||
		cfg.Duration("index.retry_unavailable") != 15*time.Minute {
		t.Fatal("defaults not applied")
	}
}

func TestLayeringFileThenEnv(t *testing.T) {
	cfg, err := load(t, `{"log.level":"debug","log.file.max_files":7}`,
		"MAILSENSE_LOG_FILE_MAX_FILES=9", "MAILSENSE_HTTP_RETRY_STATUSES=[429,503]", "OTHER=x",
		"MAILSENSE_DATA_DIR=/ignored", "MAILSENSE_SECRET_FOO=bar")
	if err != nil {
		t.Fatal(err)
	}
	snap := cfg.Snapshot()
	if snap.String("log.level") != "debug" || snap.Layer("log.level") != LayerFile {
		t.Fatal("file layer not applied")
	}
	if snap.Int("log.file.max_files") != 9 || snap.Layer("log.file.max_files") != LayerEnv {
		t.Fatal("env must override file")
	}
	if got := snap.Ints("http.retry.statuses"); len(got) != 2 || got[1] != 503 {
		t.Fatalf("env list: %v", got)
	}
}

func TestValidationReportsEveryProblemAtOnce(t *testing.T) {
	_, err := load(t, `{
		"nope.unknown": 1,
		"log.console.enabled": 1,
		"log.file.max_files": true,
		"log.file.max_size_mb": 2.5,
		"log.ring.size": 5,
		"log.level": "loud",
		"http.retry.jitter": "0.1",
		"store.busy_timeout": 5,
		"http.retry.statuses": [429, "503"],
		"log.redact.patterns": ["("],
		"log.components": ["store=loud", "Bad"],
		"http.proxy_url": "ftp://secret-host",
		"log.file.max_files": 1e2
	}`, "MAILSENSE_LOG_LEVEL=nope", "MAILSENSE_NOT_A_SETTING=1", "MAILSENSE_LOG_CONSOLE_ENABLED=1",
		"MAILSENSE_SYNC_SKIP_RATIO_FATAL=NaN")
	problems := ProblemsOf(err)
	want := []string{"nope.unknown", "log.console.enabled", "log.file.max_files", "log.file.max_size_mb",
		"log.ring.size", "log.level", "http.retry.jitter", "store.busy_timeout", "http.retry.statuses",
		"log.redact.patterns", "log.components", "http.proxy_url", "MAILSENSE_NOT_A_SETTING", "sync.skip_ratio_fatal"}
	got := map[string]int{}
	for _, p := range problems {
		got[p.Key]++
		if strings.Contains(p.Message, "secret-host") {
			t.Errorf("secret value leaked into problem: %s", p)
		}
	}
	for _, k := range want {
		if got[k] == 0 {
			t.Errorf("missing problem for %s (got %v)", k, problems)
		}
	}
	if got["log.level"] != 2 || got["log.console.enabled"] != 2 {
		t.Errorf("file and env problems for the same key must both be reported: %v", problems)
	}
}

func TestMalformedFiles(t *testing.T) {
	for name, body := range map[string]string{
		"not json":   `{`,
		"array":      `[1,2]`,
		"trailing":   `{} {}`,
		"scalar":     `"x"`,
		"nested obj": `{"log": {"level": "debug"}}`,
	} {
		if _, err := load(t, body); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if _, err := load(t, "  \n"); err != nil {
		t.Errorf("blank file is an empty layer: %v", err)
	}
	cfg, err := load(t, `{"log.level": null}`)
	if err != nil || cfg.String("log.level") != "info" {
		t.Errorf("null means default: %v", err)
	}
}

func TestSetSaveReloadAndEvents(t *testing.T) {
	bus := events.New()
	sub, _ := events.Subscribe(bus, TopicChanged, 8)
	dir := t.TempDir()
	cfg, err := Load(Options{Paths: Paths{DataDir: dir}, Bus: bus})
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Set("log.level", "debug"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Set("store.busy_timeout", "2s"); err != nil {
		t.Fatal(err)
	}
	ch := <-sub.C
	if len(ch.Keys) != 1 || ch.Keys[0] != "log.level" || len(ch.RestartRequired) != 0 {
		t.Fatalf("change: %+v", ch)
	}
	if ch = <-sub.C; len(ch.RestartRequired) != 1 {
		t.Fatalf("store.busy_timeout requires restart: %+v", ch)
	}
	for _, bad := range []struct {
		key string
		v   any
	}{{"log.level", "loud"}, {"log.level", 3}, {"nope.key", 1}, {"log.file.max_files", 1.5}, {"log.console.enabled", "true"}, {"store.busy_timeout", time.Second}} {
		if err := cfg.Set(bad.key, bad.v); err == nil {
			t.Errorf("Set(%s, %v) must fail", bad.key, bad.v)
		}
	}
	reopened, err := Load(Options{Paths: Paths{DataDir: dir}})
	if err != nil || reopened.String("log.level") != "debug" || reopened.Duration("store.busy_timeout") != 2*time.Second {
		t.Fatalf("saved values not reloaded: %v", err)
	}
	// An invalid edit on disk keeps the old values in effect.
	_ = os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"log.level":"loud"}`), 0o600)
	if err := cfg.Reload(); err == nil || cfg.String("log.level") != "debug" {
		t.Fatal("failed reload must keep previous values")
	}
	_ = os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"log.level":"warn"}`), 0o600)
	if err := cfg.Reload(); err != nil || cfg.String("log.level") != "warn" {
		t.Fatalf("reload: %v", err)
	}
	if err := cfg.Reset("log.level"); err != nil || cfg.String("log.level") != "info" {
		t.Fatalf("reset: %v", err)
	}
	if err := cfg.Reset("nope.key"); err == nil {
		t.Fatal("reset of unknown key must fail")
	}
}

func TestUILanguageSetting(t *testing.T) {
	// Fresh install: automatic follows the operating system, no file write.
	cfg, err := load(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.String("ui.language"); got != "auto" {
		t.Fatalf("default ui.language = %q, want auto", got)
	}
	if layer := cfg.Snapshot().Layer("ui.language"); layer != LayerDefault {
		t.Fatalf("default layer = %q", layer)
	}
	if pending := cfg.RestartPending(); len(pending) != 0 {
		t.Fatalf("ui.language must not require a restart: %v", pending)
	}

	// File round-trip and reset.
	dir := t.TempDir()
	cfg, err = Load(Options{Paths: Paths{DataDir: dir}})
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Set("ui.language", "fr"); err != nil {
		t.Fatal(err)
	}
	if cfg.String("ui.language") != "fr" || cfg.Snapshot().Layer("ui.language") != LayerFile {
		t.Fatalf("set: %q %q", cfg.String("ui.language"), cfg.Snapshot().Layer("ui.language"))
	}
	reopened, err := Load(Options{Paths: Paths{DataDir: dir}})
	if err != nil || reopened.String("ui.language") != "fr" {
		t.Fatalf("persisted value: %v %q", err, reopened.String("ui.language"))
	}
	if err := reopened.Reset("ui.language"); err != nil || reopened.String("ui.language") != "auto" {
		t.Fatalf("reset: %v %q", err, reopened.String("ui.language"))
	}

	// Enum validation: an unsupported language is refused, and nothing is written.
	for _, value := range []any{"es", "", true, 1} {
		if err := cfg.Set("ui.language", value); err == nil {
			t.Errorf("Set(ui.language, %#v) must fail", value)
		}
	}
	if got := cfg.String("ui.language"); got != "fr" {
		t.Fatalf("failed set changed the value: %q", got)
	}

	// Environment overrides the file, as every setting does.
	cfg, err = load(t, `{"ui.language":"en"}`, "MAILSENSE_UI_LANGUAGE=fr")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.String("ui.language") != "fr" || cfg.Snapshot().Layer("ui.language") != LayerEnv {
		t.Fatalf("env: %q %q", cfg.String("ui.language"), cfg.Snapshot().Layer("ui.language"))
	}
	if _, err := load(t, "", "MAILSENSE_UI_LANGUAGE=es"); err == nil {
		t.Fatal("invalid environment value must be refused")
	}
}

func TestLearningTestModeIsLiveAndReversible(t *testing.T) {
	// Fresh install: off, at the default layer, no restart.
	cfg, err := load(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Bool("learning.test_mode") {
		t.Fatal("the test preview must be off by default")
	}
	if layer := cfg.Snapshot().Layer("learning.test_mode"); layer != LayerDefault {
		t.Fatalf("default layer = %q", layer)
	}
	if pending := cfg.RestartPending(); len(pending) != 0 {
		t.Fatalf("learning.test_mode must not require a restart: %v", pending)
	}

	// It applies live, round-trips through settings.json, and is reversible.
	dir := t.TempDir()
	cfg, err = Load(Options{Paths: Paths{DataDir: dir}})
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Set("learning.test_mode", true); err != nil {
		t.Fatal(err)
	}
	if !cfg.Bool("learning.test_mode") || cfg.Snapshot().Layer("learning.test_mode") != LayerFile {
		t.Fatalf("set: %v %q", cfg.Bool("learning.test_mode"), cfg.Snapshot().Layer("learning.test_mode"))
	}
	if pending := cfg.RestartPending(); len(pending) != 0 {
		t.Fatalf("live setting reported a restart: %v", pending)
	}
	reopened, err := Load(Options{Paths: Paths{DataDir: dir}})
	if err != nil || !reopened.Bool("learning.test_mode") {
		t.Fatalf("persisted value: %v %v", reopened, err)
	}
	if err := reopened.Set("learning.test_mode", false); err != nil || reopened.Bool("learning.test_mode") {
		t.Fatalf("reversible: %v %v", err, reopened.Bool("learning.test_mode"))
	}
	if err := reopened.Reset("learning.test_mode"); err != nil || reopened.Bool("learning.test_mode") {
		t.Fatalf("reset: %v %v", err, reopened.Bool("learning.test_mode"))
	}

	// A non-boolean value is refused and changes nothing.
	if err := cfg.Set("learning.test_mode", "true"); err == nil || !cfg.Bool("learning.test_mode") {
		t.Fatalf("invalid value must be refused: %v %v", err, cfg.Bool("learning.test_mode"))
	}

	// The environment layer applies too.
	cfg, err = load(t, "", "MAILSENSE_LEARNING_TEST_MODE=true")
	if err != nil || !cfg.Bool("learning.test_mode") || cfg.Snapshot().Layer("learning.test_mode") != LayerEnv {
		t.Fatalf("env: %v %v %q", err, cfg.Bool("learning.test_mode"), cfg.Snapshot().Layer("learning.test_mode"))
	}
}

func TestSecretsHiddenAndCollected(t *testing.T) {
	cfg, err := load(t, `{"http.proxy_url":"http://user:pw@proxy:8080"}`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Snapshot().Display("http.proxy_url") == "http://user:pw@proxy:8080" {
		t.Fatal("secret displayed")
	}
	if got := cfg.SecretValues(); len(got) != 1 || got[0] != "http://user:pw@proxy:8080" {
		t.Fatalf("secret values: %v", got)
	}
}

func TestAccessorMisusePanics(t *testing.T) {
	cfg, _ := load(t, "")
	for name, f := range map[string]func(){
		"unknown key": func() { cfg.Int("nope.key") },
		"wrong type":  func() { cfg.Bool("log.level") },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: expected panic", name)
				}
			}()
			f()
		}()
	}
}

func TestSchemaSelfValidation(t *testing.T) {
	bad := map[string]string{
		"bad key":        `{"settings":[{"key":"Bad","type":"bool","default":true,"description":"d","group":"g"}]}`,
		"duplicate":      `{"settings":[{"key":"a.b","type":"bool","default":true,"description":"d","group":"g"},{"key":"a.b","type":"bool","default":true,"description":"d","group":"g"}]}`,
		"env collision":  `{"settings":[{"key":"a.b_c","type":"bool","default":true,"description":"d","group":"g"},{"key":"a_b.c","type":"bool","default":true,"description":"d","group":"g"}]}`,
		"reserved env":   `{"settings":[{"key":"data.dir","type":"string","default":"","description":"d","group":"g"}]}`,
		"default range":  `{"settings":[{"key":"a.b","type":"int","default":5,"min":10,"description":"d","group":"g"}]}`,
		"unknown type":   `{"settings":[{"key":"a.b","type":"map","default":{},"description":"d","group":"g"}]}`,
		"unknown field":  `{"settings":[{"key":"a.b","type":"bool","default":true,"description":"d","group":"g","extra":1}]}`,
		"bad pattern":    `{"settings":[{"key":"a.b","type":"string","default":"","pattern":"(","description":"d","group":"g"}]}`,
		"min on bool":    `{"settings":[{"key":"a.b","type":"bool","default":true,"min":1,"description":"d","group":"g"}]}`,
		"bad pair ref":   `{"settings":[{"key":"a.b","type":"list","item_type":"string","default":[],"format":"pair:x.y","description":"d","group":"g"}]}`,
		"bad item type":  `{"settings":[{"key":"a.b","type":"list","item_type":"bool","default":[],"description":"d","group":"g"}]}`,
		"pair default":   `{"settings":[{"key":"a.b","type":"list","item_type":"string","default":["x=loud"],"format":"pair:a.c","description":"d","group":"g"},{"key":"a.c","type":"enum","enum":["info"],"default":"info","description":"d","group":"g"}]}`,
		"unknown format": `{"settings":[{"key":"a.b","type":"string","default":"","format":"email","description":"d","group":"g"}]}`,
	}
	for name, doc := range bad {
		if _, err := ParseSchema([]byte(doc)); err == nil {
			t.Errorf("%s: schema must be rejected", name)
		}
	}
}

func TestResolvePaths(t *testing.T) {
	cases := []struct {
		env        []string
		goos, home string
		want       string
		fail       bool
	}{
		{[]string{"MAILSENSE_DATA_DIR=/x/y/"}, "linux", "/h", "/x/y", false},
		{[]string{`LOCALAPPDATA=C:\Users\me\AppData\Local`}, "windows", "", filepath.Join(`C:\Users\me\AppData\Local`, "Mailsense"), false},
		{nil, "windows", "", "", true},
		{[]string{"XDG_DATA_HOME=/xdg"}, "linux", "/h", "/xdg/mailsense", false},
		{[]string{"XDG_DATA_HOME=relative"}, "linux", "/h", "/h/.local/share/mailsense", false},
		{nil, "linux", "", "", true},
	}
	for _, c := range cases {
		if c.goos == "windows" && !c.fail && runtime.GOOS != "windows" {
			continue // a drive-letter path is only absolute on Windows
		}
		p, err := ResolvePaths(c.env, c.goos, c.home)
		if (err != nil) != c.fail || p.DataDir != c.want {
			t.Errorf("%v %s: got %q, %v", c.env, c.goos, p.DataDir, err)
		}
	}
	p := Paths{DataDir: "/d"}
	if p.Resolve("a.db") != "/d/a.db" || p.Resolve("/abs.db") != "/abs.db" || p.SettingsFile() != "/d/settings.json" {
		t.Fatal("resolve")
	}
}

// Change.Has is the one answer to "did one of my settings change?": a
// subscriber that spelled the loop itself skipped its trigger when a key was
// added in one place and not the other.
func TestChangeHas(t *testing.T) {
	cases := []struct {
		name  string
		keys  []string
		want  []string
		match bool
	}{
		{"no change", nil, []string{"a.b"}, false},
		{"one of them", []string{"x"}, []string{"a.b", "x"}, true},
		{"none of them", []string{"x", "y"}, []string{"a.b"}, false},
		{"several wanted", []string{"y", "b.c"}, []string{"a.b", "b.c"}, true},
	}
	for _, c := range cases {
		change := Change{Keys: c.keys}
		if got := change.Has(c.want...); got != c.match {
			t.Errorf("%s: Has(%v) over %v = %v, want %v", c.name, c.want, c.keys, got, c.match)
		}
	}
}

// Preview is nil-safe: a service built without configuration is not in
// preview, rather than panicking or disagreeing with the machine.
func TestPreviewIsNilSafe(t *testing.T) {
	var off *Config
	if off.Preview() {
		t.Fatal("a nil config reports preview on")
	}
	cfg, err := load(t, `{"learning.test_mode": true}`)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Preview() {
		t.Fatal("the configured preview is off")
	}
}

// The subscriber buffer is a config fact with one name: consumers ask for
// the count instead of spelling the key and its default again.
func TestEventBufferReadsTheSetting(t *testing.T) {
	cfg, err := load(t, `{"events.buffer_size": 64}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.EventBuffer(); got != 64 {
		t.Fatalf("cfg.EventBuffer() = %d, want 64", got)
	}
	if got := cfg.Snapshot().EventBuffer(); got != 64 {
		t.Fatalf("snapshot.EventBuffer() = %d, want 64", got)
	}
}
