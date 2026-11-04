//go:build windows

package main

import (
	"context"
	"runtime"

	"github.com/LLM-4-People/mailsense/internal/desktop"
)

// runUI creates the WebView2 window and runs it until the window closes or
// the app shuts down. Both the creation and the message loop stay on one
// locked OS thread, as WebView2 requires; Destroy posts the close message
// and is safe from the shutdown goroutine.
func runUI(url string, shutdown *shutdownRequest) (bool, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	window, err := desktop.New(url)
	if err != nil {
		return false, err
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-shutdown.Done():
			window.Destroy()
		case <-done:
		}
	}()
	window.Run()
	close(done)
	_ = shutdown.Request(context.Background())
	return true, nil
}
