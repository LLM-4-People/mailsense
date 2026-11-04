package fixturetest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/fsx"
)

// TestOpenVectorCacheRepairsTheDirectoryPerm: the cache directory is private
// through fsx.EnsureDir, which also repairs a pre-existing directory left
// group/world accessible by an old or restored fixture.
func TestOpenVectorCacheRepairsTheDirectoryPerm(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	c, err := OpenVectorCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != fsx.DirPerm {
		t.Fatalf("cache dir mode = %o, want %o", got, fsx.DirPerm)
	}
}
