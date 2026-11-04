//go:build !windows

package sidecar

import (
	"errors"
	"syscall"
)

// processGone reports whether pid no longer exists (signal 0 only probes).
func processGone(pid int) bool {
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}
