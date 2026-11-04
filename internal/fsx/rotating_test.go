package fsx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func line(n int) []byte { return []byte(strings.Repeat("x", n) + "\n") }

func TestRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	r, err := OpenRotating(path, 200, 3)
	if err != nil {
		t.Fatal(err)
	}
	for range 40 {
		if err := r.Write(line(40)); err != nil {
			t.Fatal(err)
		}
	}
	files, _ := filepath.Glob(path + "*")
	if len(files) != 3 {
		t.Fatalf("want 3 files (active + 2 rotated), got %v", files)
	}
	for _, f := range files {
		if st, _ := os.Stat(f); st.Size() > 200 {
			t.Errorf("%s exceeds the size cap: %d", f, st.Size())
		}
	}
	r.SetLimits(200, 1)
	for range 10 {
		_ = r.Write(line(40))
	}
	if files, _ = filepath.Glob(path + "*"); len(files) != 1 {
		t.Fatalf("shrinking max_files must remove rotated files: %v", files)
	}
	_ = r.Close()
	_ = r.Close()
	if err := r.Write(line(1)); err != nil {
		t.Fatalf("a write after Close reopens the file: %v", err)
	}
	_ = r.Close()
}

// Review 1a bug 2: a failed rotation closed the file for good.
func TestRotationFailureDoesNotDisableFileLogging(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	if err := os.MkdirAll(filepath.Join(dir, "app.log.1", "x"), DirPerm); err != nil { // renaming over it fails
		t.Fatal(err)
	}
	r, err := OpenRotating(path, 200, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	sawFailure := false
	for range 4 {
		sawFailure = r.Write(line(60)) != nil || sawFailure
	}
	if !sawFailure {
		t.Fatal("the blocked rotation must be reported")
	}
	_ = os.RemoveAll(filepath.Join(dir, "app.log.1"))
	for i := range 4 {
		if err := r.Write(line(60)); err != nil {
			t.Fatalf("write %d after the obstacle is gone: %v", i, err)
		}
	}
	if st, err := os.Stat(filepath.Join(dir, "app.log.1")); err != nil || st.IsDir() {
		t.Fatalf("rotation never resumed: %v", err)
	}
}

func TestOpenRotatingFailsOnBadDir(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(blocker, nil, FilePerm)
	if _, err := OpenRotating(filepath.Join(blocker, "app.log"), 10, 2); err == nil {
		t.Fatal("a log dir that is a file must fail")
	}
}
