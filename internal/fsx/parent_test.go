package fsx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRootWritersCreateTheirParent: both root-confined writers create a nested
// name's parent inside the root and need none for a name at the root. They used
// to spell the same MkdirAll block themselves.
func TestRootWritersCreateTheirParent(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	for _, name := range []string{"top.txt", filepath.Join("a", "b", "c.txt")} {
		if _, err := WriteStreamIn(root, name, strings.NewReader("payload"), FilePerm); err != nil {
			t.Fatalf("WriteStreamIn(%q): %v", name, err)
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(data) != "payload" {
			t.Fatalf("ReadFile(%q) = %q, %v", name, data, err)
		}
	}

	link := filepath.Join("links", "deep", "l")
	if err := SymlinkIn(root, "target", link); err != nil {
		t.Fatalf("SymlinkIn(%q): %v", link, err)
	}
	parent := filepath.Join(dir, "links", "deep")
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		t.Fatalf("the symlink's parent was not created: %v %v", info, err)
	}
	if got := info.Mode().Perm(); got != DirPerm {
		t.Fatalf("parent permission %v, want %v", got, DirPerm)
	}
	if target, err := os.Readlink(filepath.Join(dir, link)); err != nil || target != "target" {
		t.Fatalf("link reads %q, %v", target, err)
	}

	// A name at the root itself must not be turned into a nested path.
	if err := mkdirParentIn(root, "plain"); err != nil {
		t.Fatalf("mkdirParentIn(root name): %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".")); err != nil {
		t.Fatal(err)
	}
}

// TestRootParentCreationHasOneWriter: the root-relative MkdirAll lives in
// mkdirParentIn, so a third writer cannot drift the permission or the
// confinement by copying the block.
func TestRootParentCreationHasOneWriter(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	writers := map[string]int{}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(string(data), "root.MkdirAll("); n > 0 {
			writers[file] = n
		}
	}
	if len(writers) != 1 || writers["stream.go"] != 1 {
		t.Fatalf("root.MkdirAll is written in %v; one call belongs to mkdirParentIn", writers)
	}
}
