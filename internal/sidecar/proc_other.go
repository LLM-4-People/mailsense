//go:build !windows && !linux

package sidecar

import "os/exec"

// start runs the child (no parent-death guarantee on this platform; the
// app stops it on exit).
func start(cmd *exec.Cmd) (ender, error) { return signalEnder{}, cmd.Start() }
