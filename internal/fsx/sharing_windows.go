//go:build windows

package fsx

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isSharingViolation reports the transient errors of replacing a file that
// another process holds open.
func isSharingViolation(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_ACCESS_DENIED) ||
		errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}
