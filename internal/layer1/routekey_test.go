package layer1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/evidence"
	"github.com/LLM-4-People/mailsense/internal/threads"
)

// The route key is stable, has its own namespace, and moves with every
// configured input: a route answer computed under one configuration can
// never be served under another.
func TestRouteKeyIsStableAndFollowsEveryInput(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 8, nil)
	keyOf := func() string {
		t.Helper()
		plan, err := NewPlan(w.cfg)
		if err != nil {
			t.Fatal(err)
		}
		return plan.RouteKey()
	}
	base := keyOf()
	if !strings.HasPrefix(base, routeKeyPrefix) || len(base) != len(routeKeyPrefix)+32 {
		t.Fatalf("route key %q is not %s + 32 hex", base, routeKeyPrefix)
	}
	if again := keyOf(); again != base {
		t.Fatalf("two plans over one configuration disagree: %s != %s", base, again)
	}

	changes := []struct {
		name    string
		setting string
		value   any
	}{
		{"certify fraction", SettingCertifyFraction, 0.4},
		{"folder roles", "triage.folder_roles", []any{"inbox"}},
		{"evidence z", evidence.SettingRulesZ, 2.5},
		{"feature buckets", evidence.SettingFeaturesHashBuckets, 8},
		{"time zone", "time.zone", "America/New_York"},
		{"thread buckets", threads.SettingBuckets, []any{30, 120}},
	}
	prev := base
	for _, c := range changes {
		if err := w.cfg.Set(c.setting, c.value); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got := keyOf()
		if got == prev {
			t.Errorf("%s did not move the route key", c.name)
		}
		prev = got
	}

	// The requirements document is an input even when the profile, the
	// decider and every setting are unchanged.
	if err := writeRequirements(w.cfg, `{"defaults": {"min_eval_labels": 7, "family_wise": false}}`); err != nil {
		t.Fatal(err)
	}
	if got := keyOf(); got == prev {
		t.Fatal("a requirements edit did not move the route key")
	}
}

// The legacy question-spec fingerprint is not the route key: the route key
// must also cover the parts of the profile that are not questions.
func TestRouteKeyIsNotTheLegacyFingerprint(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 8, nil)
	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if plan.RouteKey() == plan.Profile.Fingerprint {
		t.Fatal("the route key is the legacy question-spec fingerprint")
	}
}

// Every input of the route key starts a run. Two lists that could drift
// would blank the route without rebuilding it.
func TestEveryRouteKeySettingTriggersARun(t *testing.T) {
	t.Parallel()
	triggers := make(map[string]bool, len(triggerKeys))
	for _, k := range triggerKeys {
		triggers[k] = true
	}
	for _, k := range routeKeySettingKeys {
		if !triggers[k] {
			t.Errorf("%q moves the route key but does not start a run", k)
		}
	}
}

// The bins document is a training input even when its path setting did not
// change: editing the file in place must move the key.
func TestRouteKeyFollowsTheBinsDocument(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 8, nil)
	plan, err := NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	before := plan.RouteKey()
	path := filepath.Join(t.TempDir(), "features.json")
	if err := os.WriteFile(path, []byte(`{"bins":{"question_marks":[2,5]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := w.cfg.Set(evidence.SettingFeaturesFile, path); err != nil {
		t.Fatal(err)
	}
	plan, err = NewPlan(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if plan.RouteKey() == before {
		t.Fatal("a bins edit did not move the route key")
	}
}

// The memo is content-exact: the requirements file is rewritten with the same
// length and its old mtime, and the key must still move. A stamp-based cache
// would serve the previous key here.
func TestRouteKeyMemoIsContentExact(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 4, nil)
	before, err := RouteKey(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := RouteKey(w.cfg); err != nil || again != before {
		t.Fatalf("a warm read disagrees: %q vs %q (%v)", again, before, err)
	}
	path := w.cfg.Paths().Resolve(w.cfg.String(SettingRequirementsFile))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRequirements(w.cfg, `{"defaults": {"min_eval_labels": 7, "family_wise": false}}`); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := RouteKey(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if after == before {
		t.Fatal("a same-size, same-mtime rewrite reused the cached route key")
	}
}

// A settings change moves the key even when every file is unchanged.
func TestRouteKeyMemoFollowsASettingChange(t *testing.T) {
	t.Parallel()
	w := newWorld(t, 4, nil)
	before, err := RouteKey(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.cfg.Set(SettingCertifyFraction, 0.4); err != nil {
		t.Fatal(err)
	}
	after, err := RouteKey(w.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if after == before {
		t.Fatal("a route setting change reused the cached route key")
	}
}
