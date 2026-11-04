package taxonomy

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/config/configtest"
)

// writeProfile writes one profile file with an explicit stamp, so a test can
// prove the cache follows the bytes and not the file metadata.
func writeProfile(t *testing.T, path, body string, stamp time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if !stamp.IsZero() {
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
}

// An edit that keeps the same size and mtime defeats a size+mtime stamp; the
// cache key is the merged bytes, so the profile is rebuilt anyway.
func TestLoadSeesAnEditWithTheSameSizeAndModTime(t *testing.T) {
	cfg := configtest.New(t, map[string]any{ProfileFileSetting: "profile.json"})
	path := filepath.Join(cfg.Paths().DataDir, "profile.json")
	stamp := time.Unix(1_700_000_000, 0)
	writeProfile(t, path, `{"body_chars": 1500}`, stamp)
	p1, err := Load(cfg)
	if err != nil || p1.BodyChars != 1500 {
		t.Fatalf("first load: %v %+v", err, p1)
	}
	writeProfile(t, path, `{"body_chars": 1600}`, stamp)
	p2, err := Load(cfg)
	if err != nil || p2.BodyChars != 1600 {
		t.Fatalf("stale profile after a same-stamp edit: %v %+v", err, p2)
	}
	if p2 == p1 {
		t.Fatal("the cache served the previous profile")
	}
}

// The setting names the file; changing it must rebuild even when both files
// keep their own stamp.
func TestLoadFollowsTheProfileSetting(t *testing.T) {
	cfg := configtest.New(t, map[string]any{ProfileFileSetting: "a.json"})
	writeProfile(t, filepath.Join(cfg.Paths().DataDir, "a.json"), `{"body_chars": 1111}`, time.Time{})
	p1, err := Load(cfg)
	if err != nil || p1.BodyChars != 1111 {
		t.Fatalf("a: %v %+v", err, p1)
	}
	writeProfile(t, filepath.Join(cfg.Paths().DataDir, "b.json"), `{"body_chars": 2222}`, time.Time{})
	if err := cfg.Set(ProfileFileSetting, "b.json"); err != nil {
		t.Fatal(err)
	}
	p2, err := Load(cfg)
	if err != nil || p2.BodyChars != 2222 || p2.Source != filepath.Join(cfg.Paths().DataDir, "b.json") {
		t.Fatalf("the setting change served the old profile: %v %+v", err, p2)
	}
}

// The parse is cached while nothing changes, and rebuilt when the file does.
func TestLoadBuildsOnceForAnUnchangedConfig(t *testing.T) {
	cfg := configtest.New(t, map[string]any{ProfileFileSetting: "profile.json"})
	path := filepath.Join(cfg.Paths().DataDir, "profile.json")
	writeProfile(t, path, `{"body_chars": 1500}`, time.Time{})
	p1, err := Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if p1 != p2 {
		t.Fatal("rebuilt an unchanged profile")
	}
	writeProfile(t, path, `{"body_chars": 1600}`, time.Time{})
	p3, err := Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if p3 == p1 || p3.BodyChars != 1600 {
		t.Fatal("a changed file was not rebuilt")
	}
}

// A profile error is cached with the bytes that produced it, and a fixed
// file recovers on the next read.
func TestLoadCachesAnInvalidProfileOnlyUntilItChanges(t *testing.T) {
	cfg := configtest.New(t, map[string]any{ProfileFileSetting: "profile.json"})
	path := filepath.Join(cfg.Paths().DataDir, "profile.json")
	writeProfile(t, path, `{"body_chars": 1.5}`, time.Time{})
	if _, err := Load(cfg); err == nil {
		t.Fatal("an invalid profile must fail")
	}
	if _, err := Load(cfg); err == nil {
		t.Fatal("the cached error must still be an error")
	}
	writeProfile(t, path, `{"body_chars": 900}`, time.Time{})
	if p, err := Load(cfg); err != nil || p.BodyChars != 900 {
		t.Fatalf("a fixed file must recover: %v %+v", err, p)
	}
}
