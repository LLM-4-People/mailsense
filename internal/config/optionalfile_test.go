package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestUnreadableConfigFilesStayProblems: both optional config files are read
// through fsx.ReadOptional, whose contract is "absent is empty, any other
// failure is an error". A path that exists but cannot be read as a file (here
// a directory) must therefore become a problem naming it, never an absent,
// empty layer: the two used to spell the os.ReadFile/ErrNotExist check
// themselves, and a local "any error means absent" would silently drop a
// broken data directory.
func TestUnreadableConfigFilesStayProblems(t *testing.T) {
	for _, tc := range []struct {
		name string
		file func(Paths) string
	}{
		{"settings.json", Paths.SettingsFile},
		{"install-profile.json", Paths.InstallProfileFile},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths := Paths{DataDir: t.TempDir()}
			path := tc.file(paths)
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			_, err := Load(Options{Paths: paths})
			if err == nil {
				t.Fatalf("a directory at %s was treated as an absent file", path)
			}
			problems := ProblemsOf(err)
			found := false
			for _, p := range problems {
				if p.Source == path {
					found = true
				}
			}
			if !found {
				t.Fatalf("no problem names %s: %+v", path, problems)
			}
		})
	}

	// The existing behaviour of a genuinely absent pair is unchanged: both
	// files missing is the normal first run, not a problem.
	cfg, err := Load(Options{Paths: Paths{DataDir: t.TempDir()}})
	if err != nil {
		t.Fatalf("an absent settings file and profile became a problem: %v", err)
	}
	if cfg.Snapshot().Layer("log.level") != LayerDefault {
		t.Fatal("an absent file did not fall back to the default layer")
	}
}

// TestProfileDocWithUnreadableFile: the edit path reads the same optional file
// and must return the failure instead of editing an empty document (which
// would overwrite a profile it could not read).
func TestProfileDocWithUnreadableFile(t *testing.T) {
	paths := Paths{DataDir: t.TempDir()}
	if err := os.Mkdir(Paths.InstallProfileFile(paths), 0o700); err != nil {
		t.Fatal(err)
	}
	c := &Config{opts: Options{Paths: paths}}
	if _, err := c.readProfileDoc(); err == nil {
		t.Fatal("a directory at the install profile was read as a missing document")
	}
	if _, err := os.Stat(filepath.Join(paths.DataDir, "install-profile.json")); err != nil {
		t.Fatal(err)
	}
}
