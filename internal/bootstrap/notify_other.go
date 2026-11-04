//go:build !windows

package bootstrap

import "os"

// notify: development builds have a terminal.
func notify(text string) { _, _ = os.Stderr.WriteString(text) }
