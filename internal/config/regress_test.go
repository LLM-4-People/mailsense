package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Review 1a bug 6: Set rebuilt settings.json from memory, clobbering edits.
func TestSetKeepsExternalEditsAndRefusesInvalidFile(t *testing.T) {
	p := Paths{DataDir: t.TempDir()}
	c, err := Load(Options{Paths: p})
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(p.SettingsFile(), []byte(`{"log.level":"debug","sync.batch_size":50}`), 0o600)
	if err := c.Set("jobs.history_size", 7); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p.SettingsFile())
	if !strings.Contains(string(b), `"sync.batch_size": 50`) || c.Int("sync.batch_size") != 50 {
		t.Fatalf("external edit lost: %s", b)
	}
	broken := []byte(`{"log.level":"debug", "sync.batch_size": 50,}`)
	_ = os.WriteFile(p.SettingsFile(), broken, 0o600)
	if c.Reload() == nil {
		t.Fatal("reload of a broken file must fail")
	}
	if err := c.Set("jobs.history_size", 8); err == nil {
		t.Fatal("Set must refuse to overwrite a settings file it cannot parse")
	}
	if b, _ := os.ReadFile(p.SettingsFile()); string(b) != string(broken) {
		t.Fatalf("broken file overwritten: %s", b)
	}
}

// Review 1a bug 8: a relative MAILSENSE_DATA_DIR reached SQLite as file://data/… (a host).
func TestRelativeDataDirBecomesAbsolute(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	p, err := ResolvePaths([]string{"MAILSENSE_DATA_DIR=data"}, "linux", "/home/x")
	if err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(dir)
	if !filepath.IsAbs(p.DataDir) || (p.DataDir != filepath.Join(dir, "data") && p.DataDir != filepath.Join(real, "data")) {
		t.Fatalf("data dir %q is not the absolute form", p.DataDir)
	}
	if _, err := Load(Options{Paths: Paths{DataDir: "relative"}}); err == nil {
		t.Fatal("Load must refuse a relative data dir")
	}
}

// Review 1a (low): year_at_most ≥ year_at_least made every date "no date".
func TestCrossFieldConstraints(t *testing.T) {
	_, err := load(t, `{"mail.no_date.year_at_most": 5000, "mail.no_date.year_at_least": 4000}`)
	if err == nil {
		t.Fatal("year_at_most must be below year_at_least")
	}
	_, err = load(t, `{"http.retry.base_delay": "2m", "http.retry.max_delay": "1m"}`)
	if err == nil {
		t.Fatal("base_delay above max_delay must be rejected")
	}
}

// Review 1a rules bug 5: list items of a secret setting printed their value.
func TestSecretListItemsAreHidden(t *testing.T) {
	s, err := ParseSchema([]byte(`{"settings":[{"key":"a.b","type":"list","item_type":"string","pattern":"^ok$","secret":true,"default":[],"description":"d","group":"g"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	st, _ := s.Lookup("a.b")
	_, err = st.rule.fromAny([]any{"hunter2-value"})
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("secret list item leaked or accepted: %v", err)
	}
}

func TestCheckRestartPendingAndUnwritableFile(t *testing.T) {
	dir := t.TempDir()
	c, err := Load(Options{Paths: Paths{DataDir: dir}})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Check("store.busy_timeout", "9s"); err != nil {
		t.Fatal(err)
	}
	if err := c.Check("mail.no_date.year_at_most", 9000); err == nil {
		t.Fatal("Check must apply cross-field constraints")
	}
	if c.Duration("store.busy_timeout") != 5*time.Second {
		t.Fatal("Check must not apply the value")
	}
	if _, err := os.Stat(filepath.Join(dir, "settings.json")); !os.IsNotExist(err) {
		t.Fatal("Check must not save")
	}
	if len(c.RestartPending()) != 0 {
		t.Fatal("nothing pending at start")
	}
	if err := c.Set("store.busy_timeout", "9s"); err != nil {
		t.Fatal(err)
	}
	if err := c.Set("log.level", "debug"); err != nil {
		t.Fatal(err)
	}
	if got := c.RestartPending(); len(got) != 1 || got[0] != "store.busy_timeout" {
		t.Fatalf("pending %v", got)
	}
	if err := c.Set("store.busy_timeout", "5s"); err != nil || len(c.RestartPending()) != 0 {
		t.Fatalf("back to the start value clears pending: %v %v", err, c.RestartPending())
	}
	// settings.json replaced by a directory: saving fails, values unchanged.
	_ = os.Remove(filepath.Join(dir, "settings.json"))
	_ = os.Mkdir(filepath.Join(dir, "settings.json"), 0o700)
	if err := c.Set("log.level", "warn"); err == nil || c.String("log.level") != "debug" {
		t.Fatalf("unwritable settings.json: %v, level %s", err, c.String("log.level"))
	}
}

func TestProblemFormattingAndAccessors(t *testing.T) {
	p := Problem{Key: "a.b", Source: "env X", Message: "bad"}
	if p.String() != "a.b (env X): bad" {
		t.Fatal(p.String())
	}
	err := Problems{p, p}.err()
	if !strings.Contains(err.Error(), "2 problem(s)") || len(ProblemsOf(err)) != 2 || ProblemsOf(os.ErrClosed) != nil {
		t.Fatal(err)
	}
	c, _ := load(t, `{"http.log.request_id_headers": ["a-b"]}`)
	if got := c.Strings("http.log.request_id_headers"); len(got) != 1 || got[0] != "a-b" {
		t.Fatal(got)
	}
	got := c.Strings("http.log.request_id_headers")
	got[0] = "mutated"
	if c.Strings("http.log.request_id_headers")[0] != "a-b" {
		t.Fatal("Strings must return a copy")
	}
}

func TestSchemaConstraintValidation(t *testing.T) {
	base := `{"key":"a.x","type":"int","default":1,"description":"d","group":"g"},{"key":"a.y","type":"int","default":2,"description":"d","group":"g"},{"key":"a.s","type":"string","default":"","description":"d","group":"g"}`
	for name, cons := range map[string]string{
		"unknown key": `{"key":"a.x","op":"<","other":"a.zz"}`,
		"bad op":      `{"key":"a.x","op":"!=","other":"a.y"}`,
		"type":        `{"key":"a.x","op":"<","other":"a.s"}`,
	} {
		if _, err := ParseSchema([]byte(`{"settings":[` + base + `],"constraints":[` + cons + `]}`)); err == nil {
			t.Errorf("%s: constraint must be rejected", name)
		}
	}
	if _, err := ParseSchema([]byte(`{"settings":[` + base + `],"constraints":[{"key":"a.y","op":"<","other":"a.x"}]}`)); err != nil {
		t.Fatal("constraints are checked on effective values, not at parse time:", err)
	}
}

// No inert settings: every schema key is read by production code.
func TestEverySettingIsUsed(t *testing.T) {
	s, _ := Embedded()
	var sources strings.Builder
	for _, root := range []string{"../../internal", "../../cmd"} {
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") && !strings.Contains(p, "configtest") {
				b, _ := os.ReadFile(p)
				sources.Write(b)
			}
			return nil
		})
	}
	// The embedded shell is production code too: some settings (the interface
	// language) are consumed by the UI through the settings API rather than by
	// a Go call site.
	for _, p := range []string{"../../internal/ui/app.js", "../../internal/ui/index.html"} {
		if b, err := os.ReadFile(p); err == nil {
			sources.Write(b)
		}
	}
	all := sources.String()
	for _, st := range s.Settings() {
		if !strings.Contains(all, `"`+st.Key+`"`) {
			t.Errorf("setting %s is not read by any production code (wire it or remove it)", st.Key)
		}
	}
}

// Review 2a #6: not_in forbids values of int settings and int list items.
func TestNotInRule(t *testing.T) {
	bad := `{"settings": [{"key": "a.b", "type": "string", "default": "x", "not_in": [1], "group": "G", "description": "d"}]}`
	if _, err := ParseSchema([]byte(bad)); err == nil {
		t.Fatal("not_in on a string is a schema error")
	}
	ok := `{"settings": [{"key": "a.b", "type": "list", "item_type": "int", "default": [2], "not_in": [1], "group": "G", "description": "d"}]}`
	s, err := ParseSchema([]byte(ok))
	if err != nil {
		t.Fatal(err)
	}
	st, _ := s.Lookup("a.b")
	if _, err := st.rule.fromJSON([]byte(`[2, 1]`)); err == nil {
		t.Fatal("a forbidden item must be refused")
	}
	bad = `{"settings": [{"key": "a.b", "type": "list", "item_type": "int", "default": [1], "not_in": [1], "group": "G", "description": "d"}]}`
	if _, err := ParseSchema([]byte(bad)); err == nil {
		t.Fatal("a default in not_in is a schema error")
	}
}

// The install profile (user decision 2026-10-15): learned values sit
// between the default and the user's override, carry sample size and
// provenance, are persisted and queryable; only learnable settings take
// them; stale entries are shown, never applied.
func TestInstallProfileLayer(t *testing.T) {
	dir := t.TempDir()
	c := loadIn(t, dir)
	prov := Provenance{Learner: "mail.domains", Basis: map[string]int{"senders": 1200, "threads": 4100}, LearnedAt: "2026-10-15T08:00:00Z"}
	if err := c.Learn("mail.domains.shared", []string{"example.net"}, 1200, prov); err != nil {
		t.Fatal(err)
	}
	snap := c.Snapshot()
	if got := snap.Strings("mail.domains.shared"); len(got) != 1 || got[0] != "example.net" || snap.Layer("mail.domains.shared") != LayerLearned {
		t.Fatalf("applied: %v %s", got, snap.Layer("mail.domains.shared"))
	}
	if l := c.Learned(); len(l) != 1 || l[0].Sample != 1200 || !reflect.DeepEqual(l[0].Provenance, prov) || l[0].Ignored != "" {
		t.Fatalf("queryable: %+v", l)
	}
	// Persisted: a new process sees it.
	if got := loadIn(t, dir).Strings("mail.domains.shared"); len(got) != 1 {
		t.Fatalf("persisted: %v", got)
	}
	// The user's override wins and the learned value stays recorded.
	if err := c.Set("mail.domains.shared", []string{}); err != nil {
		t.Fatal(err)
	}
	if snap := c.Snapshot(); len(snap.Strings("mail.domains.shared")) != 0 || snap.Layer("mail.domains.shared") != LayerFile || len(c.Learned()) != 1 {
		t.Fatal("a user override wins over a learned value")
	}
	// Misuse: a setting that is not learnable, a bad value, no provenance.
	for name, f := range map[string]func() error{
		"not learnable": func() error { return c.Learn("log.level", "debug", 1, prov) },
		"unknown":       func() error { return c.Learn("nope", 1, 1, prov) },
		"bad value":     func() error { return c.Learn("mail.domains.shared", []string{"no dot"}, 1, prov) },
		"no learner":    func() error { return c.Learn("mail.domains.shared", []string{"a.b"}, 1, Provenance{LearnedAt: "x"}) },
		"bad sample":    func() error { return c.Learn("mail.domains.shared", []string{"a.b"}, -1, prov) },
	} {
		if err := f(); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
}

// loadIn loads the configuration of a data directory.
func loadIn(t *testing.T, dir string) *Config {
	t.Helper()
	c, err := Load(Options{Paths: Paths{DataDir: dir}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A stale install-profile entry (after an upgrade) is listed with its
// reason, never applied; a file that does not parse is a load problem and
// is never overwritten.
func TestInstallProfileStaleAndBrokenFiles(t *testing.T) {
	dir := t.TempDir()
	prov := Provenance{Learner: "mail.domains", LearnedAt: "2026-10-15T08:00:00Z"}
	stale := `{"entries": {"log.level": {"value": "debug", "sample": 1, "provenance": {"learner": "x", "basis": {}, "learned_at": "t"}},
		"mail.subject.prefixes_learned": {"value": ["wg", "回复"], "sample": 40, "provenance": {"learner": "mail.prefixes", "basis": {"replies": 40}, "learned_at": "t"}}}}`
	if err := os.WriteFile(filepath.Join(dir, installProfileFile), []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	c2 := loadIn(t, dir)
	if c2.String("log.level") != "info" || len(c2.Strings("mail.subject.prefixes_learned")) != 2 {
		t.Fatal("only learnable entries apply")
	}
	if l := c2.Learned(); len(l) != 2 || l[0].Key != "log.level" || l[0].Ignored == "" || l[1].Ignored != "" {
		t.Fatalf("stale entry shown: %+v", l)
	}
	if err := os.WriteFile(filepath.Join(dir, installProfileFile), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(Options{Paths: Paths{DataDir: dir}}); err == nil {
		t.Fatal("a broken install profile is reported")
	}
	if err := c2.Learn("mail.domains.shared", []string{"a.b"}, 1, prov); err == nil {
		t.Fatal("never overwrite a file that does not parse")
	}
}
