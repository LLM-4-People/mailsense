//go:build !windows

package sidecar

import (
	"context"
	"syscall"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock"
)

// signalEnder asks the child to stop (SIGTERM) and ends it once the grace
// period is over.
type signalEnder struct{}

func (signalEnder) end(c *child, grace time.Duration, clk clock.Clock) {
	_ = c.cmd.Process.Signal(syscall.SIGTERM)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-c.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	if clk.Sleep(ctx, grace) == nil {
		_ = c.cmd.Process.Kill()
	}
}
