package fsx

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Rotating appends lines to a file and rotates it by size:
// path → path.1 → … → path.(maxFiles-1), the oldest being removed.
// A failed rotation (Windows: a reader without FILE_SHARE_DELETE, anti-virus,
// a second instance) reopens the active file and is retried on the next write,
// so file logging never stops for good.
type Rotating struct {
	mu       sync.Mutex
	path     string
	f        *os.File
	size     int64
	maxBytes int64
	maxFiles int
}

// OpenRotating opens (creating) path for appending.
func OpenRotating(path string, maxBytes int64, maxFiles int) (*Rotating, error) {
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	r := &Rotating{path: path, maxBytes: maxBytes, maxFiles: maxFiles}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Rotating) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, FilePerm)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	r.f, r.size = f, st.Size()
	return nil
}

// SetLimits changes the size cap and file count.
func (r *Rotating) SetLimits(maxBytes int64, maxFiles int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.maxBytes, r.maxFiles = maxBytes, maxFiles
}

// Write appends one line. A rotation failure is returned but the line is
// still written to the active file.
func (r *Rotating) Write(line []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		if err := r.open(); err != nil {
			return err
		}
	}
	var rotErr error
	if r.size > 0 && r.size+int64(len(line)) > r.maxBytes {
		rotErr = r.rotate()
		if r.f == nil {
			return rotErr
		}
	}
	n, err := r.f.Write(line)
	r.size += int64(n)
	return errors.Join(rotErr, err)
}

// rotate shifts the files; on any failure the active file is reopened in
// append mode so the next write retries.
func (r *Rotating) rotate() error {
	if err := r.f.Close(); err != nil {
		return err
	}
	r.f = nil
	err := r.shift()
	if openErr := r.open(); openErr != nil {
		return errors.Join(err, openErr)
	}
	return err
}

func (r *Rotating) shift() error {
	r.removeBeyond(r.maxFiles - 1)
	for i := r.maxFiles - 2; i >= 1; i-- {
		if err := os.Rename(r.numbered(i), r.numbered(i+1)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if r.maxFiles > 1 {
		return os.Rename(r.path, r.numbered(1))
	}
	return os.Truncate(r.path, 0)
}

// removeBeyond deletes rotated files numbered ≥ keep (also after max_files shrank).
func (r *Rotating) removeBeyond(keep int) {
	dir := filepath.Dir(r.path)
	entries, _ := os.ReadDir(dir)
	prefix := filepath.Base(r.path) + "."
	for _, e := range entries {
		suffix, ok := strings.CutPrefix(e.Name(), prefix)
		if n, err := strconv.Atoi(suffix); ok && err == nil && n >= keep {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func (r *Rotating) numbered(i int) string { return r.path + "." + strconv.Itoa(i) }

// Close closes the active file; a later Write reopens it.
func (r *Rotating) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}
