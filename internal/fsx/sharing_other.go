//go:build !windows

package fsx

// isSharingViolation: POSIX renames do not fail because a reader holds the file.
func isSharingViolation(error) bool { return false }
