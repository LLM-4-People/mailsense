package fsx

import (
	"errors"
	"os"
	"path/filepath"
)

// ErrLocked means another process holds the lock.
var ErrLocked = errors.New("fsx: the file is locked by another process")

// Lock is one held exclusive advisory lock. The file is created when
// missing and left in place after Close: deleting a lock file lets two
// processes lock different inodes and both believe they are the only one.
type Lock struct {
	file *os.File
}

// LockFile takes an exclusive, non-blocking lock on path, creating it
// (and its parent directory) when needed. A second call for the same
// path from any process returns ErrLocked.
func LockFile(path string) (*Lock, error) {
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, FilePerm)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		_ = f.Close()
		if errors.Is(err, ErrLocked) {
			return nil, err
		}
		return nil, err
	}
	return &Lock{file: f}, nil
}

// Close releases the lock. Safe to call twice.
func (l *Lock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := unlockFile(l.file)
	if cerr := l.file.Close(); err == nil {
		err = cerr
	}
	l.file = nil
	return err
}
