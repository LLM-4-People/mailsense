package fsx

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestLockFileIsExclusiveAndReusable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "app.lock")
	first, err := LockFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LockFile(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("second LockFile = %v, want ErrLocked", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := LockFile(path)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
