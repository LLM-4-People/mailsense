package fsx

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock/clocktest"
)

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "a.json")
	if err := WriteFileAtomic(path, []byte("one"), FilePerm, Replace{}); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("two"), FilePerm, Replace{}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "two" {
		t.Fatalf("content %q", b)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != FilePerm {
		t.Fatalf("perm %v", st.Mode().Perm())
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}
}

func TestWriteFileAtomicFailureLeavesOldContent(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, DirPerm); err != nil { // a directory cannot be replaced by a file
		t.Fatal(err)
	}
	if err := WriteFileAtomic(target, []byte("x"), FilePerm, Replace{}); err == nil {
		t.Fatal("expected an error")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("temp file not cleaned up: %v", entries)
	}
	blocker := filepath.Join(dir, "file")
	_ = os.WriteFile(blocker, nil, FilePerm)
	if err := WriteFileAtomic(filepath.Join(blocker, "child"), []byte("x"), FilePerm, Replace{}); err == nil {
		t.Fatal("a parent that is a file must fail")
	}
}

// Review 1a (low): a rename-over blocked by a sharing violation is retried.
func TestReplaceRetriesSharingViolations(t *testing.T) {
	locked := errors.New("sharing violation")
	clk := clocktest.New(time.Unix(0, 0))
	calls := 0
	p := Replace{Attempts: 4, Delay: 50 * time.Millisecond, Clock: clk,
		retryable: func(err error) bool { return errors.Is(err, locked) },
		rename: func(a, b string) error {
			calls++
			if calls < 3 {
				return locked
			}
			return os.Rename(a, b)
		}}
	path := filepath.Join(t.TempDir(), "s.json")
	if err := WriteFileAtomic(path, []byte("x"), FilePerm, p); err != nil {
		t.Fatal(err)
	}
	if calls != 3 || len(clk.Sleeps()) != 2 || clk.Sleeps()[0] != 50*time.Millisecond {
		t.Fatalf("calls %d sleeps %v", calls, clk.Sleeps())
	}
	calls = -100 // always locked
	err := WriteFileAtomic(path, []byte("y"), FilePerm, p)
	if !errors.Is(err, locked) {
		t.Fatalf("exhausted retries must surface the cause: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "x" {
		t.Fatal("a failed replace must keep the old content")
	}
	other := Replace{Attempts: 4, Clock: clk, rename: func(string, string) error { calls++; return os.ErrPermission }}
	calls = 0
	if err := WriteFileAtomic(path, []byte("z"), FilePerm, other); err == nil || calls != 1 {
		t.Fatalf("non-sharing errors are not retried: %v after %d calls", err, calls)
	}
}

func TestEnsureDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	if err := EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(dir); !st.IsDir() || st.Mode().Perm() != DirPerm {
		t.Fatalf("dir %v", st.Mode())
	}
}

func TestRemoveAll(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "live-data")
	if err := os.MkdirAll(filepath.Join(dir, "logs"), DirPerm); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "logs", "x.log"), []byte("x"), FilePerm); err != nil {
		t.Fatal(err)
	}
	if err := RemoveAll(dir, Replace{Attempts: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("still there: %v", err)
	}
	if err := RemoveAll(dir, Replace{}); err != nil {
		t.Fatal("removing nothing is fine")
	}
}

func TestReadOptional(t *testing.T) {
	dir := t.TempDir()
	if b, ok, err := ReadOptional(filepath.Join(dir, "missing")); b != nil || ok || err != nil {
		t.Fatal("missing file")
	}
	p := filepath.Join(dir, "f")
	_ = os.WriteFile(p, []byte("x"), 0o600)
	if b, ok, err := ReadOptional(p); string(b) != "x" || !ok || err != nil {
		t.Fatal("present file")
	}
	if _, _, err := ReadOptional(dir); err == nil {
		t.Fatal("a directory is an error, not absence")
	}
}
