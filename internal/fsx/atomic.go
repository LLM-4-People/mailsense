package fsx

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock"
)

// Replace is the policy for the final rename of an atomic write. On Windows
// the rename fails with a sharing violation while another process (anti-
// virus, indexer, an editor) holds the target open; such failures are
// retried Attempts times, Delay apart. Elsewhere they never occur.
type Replace struct {
	Attempts int
	Delay    time.Duration
	Clock    clock.Clock

	// Test hooks; nil means os.Rename and the platform's sharing-violation test.
	rename    func(oldpath, newpath string) error
	retryable func(error) bool
}

// WriteFileAtomic writes data to path through a synced temp file in the same
// directory and a rename, so readers see either the old or the new content.
// Missing parent directories are created.
func WriteFileAtomic(path string, data []byte, perm os.FileMode, policy Replace) error {
	dir := filepath.Dir(path)
	if err := EnsureDir(dir); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = tmp.Close()
			_ = os.Remove(name)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, perm); err != nil {
		return err
	}
	if err := policy.replace(name, path); err != nil {
		return err
	}
	ok = true
	return nil
}

func (p Replace) replace(from, to string) error {
	rename := p.rename
	if rename == nil {
		rename = os.Rename
	}
	return p.retry("replace "+to, func() error { return rename(from, to) })
}

// RemoveAll removes path and everything below it, retrying while another
// process (anti-virus, an indexer, a just-closed SQLite handle) still holds
// a file open on Windows.
func RemoveAll(path string, p Replace) error {
	return p.retry("remove "+path, func() error { return os.RemoveAll(path) })
}

// retry runs op up to Attempts times, Delay apart, while it fails with a
// sharing violation.
func (p Replace) retry(what string, op func() error) error {
	retryable := p.retryable
	if retryable == nil {
		retryable = isSharingViolation
	}
	var err error
	for attempt := 0; attempt < max(p.Attempts, 1); attempt++ {
		if attempt > 0 {
			if serr := clock.Or(p.Clock).Sleep(context.Background(), p.Delay); serr != nil {
				return serr
			}
		}
		if err = op(); err == nil || !retryable(err) {
			return err
		}
	}
	return fmt.Errorf("%s: still locked after %d attempt(s): %w", what, max(p.Attempts, 1), err)
}
