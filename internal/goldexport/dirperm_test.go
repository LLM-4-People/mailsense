package goldexport

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/fsx"
)

// TestWriteRepairsTheDirectoryPerm: the export directory is private through
// fsx.EnsureDir, which also repairs a pre-existing directory left
// group/world accessible by an old or restored gold set.
func TestWriteRepairsTheDirectoryPerm(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gold")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// An empty file set isolates Write's own directory call: WriteFileAtomic
	// repairs the directory too, so a written file would hide it.
	if err := Write(dir, nil, fsx.Replace{}); err != nil {
		t.Fatal(err)
	}
	assertDirPerm(t, dir)
}

// TestWriteMakesAPrivateDirectoryAndFiles: a missing directory is created
// private and every exported file is private.
func TestWriteMakesAPrivateDirectoryAndFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gold")
	if err := Write(dir, map[string][]byte{"taxonomy.json": []byte("{}")}, fsx.Replace{}); err != nil {
		t.Fatal(err)
	}
	assertDirPerm(t, dir)
	file, err := os.Stat(filepath.Join(dir, "taxonomy.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := file.Mode().Perm(); got != fsx.FilePerm {
		t.Fatalf("exported file mode = %o, want %o", got, fsx.FilePerm)
	}
}

func assertDirPerm(t *testing.T, dir string) {
	t.Helper()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != fsx.DirPerm {
		t.Fatalf("export dir mode = %o, want %o", got, fsx.DirPerm)
	}
}
