//go:build linux

package sidecar

import (
	"os/exec"
	"syscall"
)

// start runs the child in its own process group with the parent-death
// signal set: the kernel ends it when the app dies.
func start(cmd *exec.Cmd) (ender, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL, Setpgid: true}
	return signalEnder{}, cmd.Start()
}
