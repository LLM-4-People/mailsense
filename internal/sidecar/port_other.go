//go:build !linux && !windows

package sidecar

// ownsPort cannot be checked on this platform (not a target of the app):
// the runtime is trusted as on loopback alone.
func ownsPort(int, int) (bool, error) { return true, nil }

// portHeld cannot be checked on this platform: a launch is never retried,
// as before.
func portHeld(int) (bool, error) { return false, nil }
