package fsx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// OpenAppend opens path for appending, creating it (and its directory)
// with FilePerm: the resumable-download write path.
func OpenAppend(path string) (*os.File, error) {
	if err := EnsureDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, FilePerm)
}

// Promote renames a finished file or directory into place (the target is
// replaced; directories must not exist), with the sharing-violation
// retries of the policy.
func Promote(from, to string, p Replace) error { return p.replace(from, to) }

// Remove deletes a file (absent is fine), retrying while another process
// holds it open on Windows.
func Remove(path string, p Replace) error {
	return p.retry("remove "+path, func() error {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	})
}

// WriteJSON writes v as indented JSON (with a final newline) through
// WriteFileAtomic: every JSON data file the app keeps.
func WriteJSON(path string, v any, perm os.FileMode, p Replace) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	return WriteFileAtomic(path, append(data, '\n'), perm, p)
}

// mkdirParentIn creates the parent directory of name inside root (a name at
// the root itself needs none). Both root-confined writers create it the same
// way; the root-relative call is what keeps the directory inside the root.
func mkdirParentIn(root *os.Root, name string) error {
	if dir := filepath.Dir(name); dir != "." {
		return root.MkdirAll(dir, DirPerm)
	}
	return nil
}

// WriteStreamIn is WriteStream for a name inside root: nothing it writes
// can land outside the root, whatever links the name passes through.
func WriteStreamIn(root *os.Root, name string, r io.Reader, perm os.FileMode) (int64, error) {
	if err := mkdirParentIn(root, name); err != nil {
		return 0, err
	}
	f, err := root.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, r)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return n, fmt.Errorf("write %s: %w", name, err)
	}
	return n, nil
}

// SymlinkIn creates a link at name inside root pointing to target.
func SymlinkIn(root *os.Root, target, name string) error {
	if err := mkdirParentIn(root, name); err != nil {
		return err
	}
	return root.Symlink(target, name)
}
