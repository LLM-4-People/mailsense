//go:build !windows

package main

import "github.com/LLM-4-People/mailsense/internal/desktop"

// runUI starts the default browser when a display exists and returns at
// once: the API stays the lifetime owner. A missing display or browser is
// reported to the caller as a warning, never a startup failure.
func runUI(url string, _ *shutdownRequest) (bool, error) {
	return false, desktop.Open(url)
}
