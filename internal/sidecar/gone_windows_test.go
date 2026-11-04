//go:build windows

package sidecar

import "golang.org/x/sys/windows"

// processGone reports whether pid no longer runs.
func processGone(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return true
	}
	defer windows.CloseHandle(h)
	var code uint32
	return windows.GetExitCodeProcess(h, &code) == nil && code != 259 // STILL_ACTIVE
}
