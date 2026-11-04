package bootstrap

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReportWritesAndClearRemoves(t *testing.T) {
	dir := t.TempDir()
	path := Report(dir, errors.New("log.level: must be one of …"))
	if path != filepath.Join(dir, ErrorFile) {
		t.Fatalf("path %q", path)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "log.level") || !strings.Contains(string(b), path) {
		t.Fatalf("report %q", b)
	}
	Clear(dir)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("Clear must remove the report")
	}
	blocker := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(blocker, nil, 0o600)
	if Report(blocker, errors.New("x")) != "" {
		t.Fatal("an unwritable data dir must be reported as unsaved")
	}
}
