package fsx

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ErrUnsafePath is the one named failure of a path that crosses a trust
// boundary: empty, containing a NUL, not cleaned, or outside the root its
// owner named.
var ErrUnsafePath = errors.New("fsx: unsafe path")

// ValidatePath enforces the invariant of a path that crosses a trust
// boundary: it must be non-empty, contain no NUL byte and already be in
// cleaned form, so a later filepath.Join cannot reinterpret a ".." element.
func ValidatePath(path string) error {
	if path == "" {
		return fmt.Errorf("%w: empty path", ErrUnsafePath)
	}
	if strings.ContainsRune(path, 0) {
		return fmt.Errorf("%w: contains a NUL byte", ErrUnsafePath)
	}
	if filepath.Clean(path) != path {
		return fmt.Errorf("%w: %q is not a cleaned path", ErrUnsafePath, path)
	}
	return nil
}

// CleanPath returns the cleaned form of path after the empty and NUL checks.
func CleanPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("%w: empty path", ErrUnsafePath)
	}
	if strings.ContainsRune(path, 0) {
		return "", fmt.Errorf("%w: contains a NUL byte", ErrUnsafePath)
	}
	return filepath.Clean(path), nil
}

// Within resolves path under root (a relative path is joined to root),
// cleans both and refuses a result that escapes root. It is the containment
// rule for names that come from configuration or a catalog: the result can
// still be absolute, but it is always inside root.
func Within(root, path string) (string, error) {
	root, err := CleanPath(root)
	if err != nil {
		return "", err
	}
	p, err := CleanPath(path)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	rel, err := filepath.Rel(root, p)
	if err != nil || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("%w: %q escapes %q", ErrUnsafePath, path, root)
	}
	return filepath.Join(root, rel), nil
}
