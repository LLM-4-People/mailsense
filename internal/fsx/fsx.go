// Package fsx is the file-system choke point: every file the application
// writes (settings, secrets, logs) goes through it.
package fsx

import "os"

// Permissions of everything Mailsense creates: private to the user.
const (
	DirPerm  os.FileMode = 0o700
	FilePerm os.FileMode = 0o600
)

// EnsureDir creates dir and its parents with DirPerm. An existing directory
// is repaired to DirPerm too: an old or restored data directory may be
// group/world accessible while holding every message body.
func EnsureDir(dir string) error {
	if err := os.MkdirAll(dir, DirPerm); err != nil {
		return err
	}
	return os.Chmod(dir, DirPerm)
}
