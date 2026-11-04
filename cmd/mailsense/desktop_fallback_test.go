package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestAFailedWindowReportsAndFallsBack: a machine without WebView2 must not
// start a headless process with no message. The report names the runtime and
// the browser fallback, and shutdown still completes.
func TestAFailedWindowReportsAndFallsBack(t *testing.T) {
	dir := t.TempDir()
	var r reports
	shutdown := newShutdownRequest()
	failing := func(string, *shutdownRequest) (bool, error) {
		return false, errors.New("desktop: cannot create the WebView2 window (is the WebView2 runtime installed?)")
	}
	done := make(chan int, 1)
	go func() {
		done <- runWithShutdownUI([]string{"MAILSENSE_DATA_DIR=" + dir}, "linux", "", r.report, shutdown, failing)
	}()
	// The failing window runs after the warm starts; the app must still
	// report and remain alive until shutdown.
	deadline := time.Now().Add(60 * time.Second)
	for {
		if r.err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a failed window was never reported")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, want := range []string{"WebView2", "browser"} {
		if !strings.Contains(r.err.Error(), want) {
			t.Errorf("window failure report %q does not mention %q", r.err, want)
		}
	}
	if err := shutdown.Request(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d after a window failure", code)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("the app did not shut down")
	}
}
