package ui

import (
	"io/fs"
	"strings"
	"testing"
)

// Every live mail frame shares one resize listener; a listener per message
// outlived its detached iframe until the next resize.
func TestMailFramesShareOneResizeListener(t *testing.T) {
	script, err := fs.ReadFile(Files, "app.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(script)
	start := strings.Index(js, "// mail-frame:start")
	end := strings.Index(js, "// mail-frame:end")
	if start < 0 || end <= start {
		t.Fatal("the mail-frame block is missing")
	}
	block := js[start:end]
	if got := strings.Count(block, `window.addEventListener("resize"`); got != 1 {
		t.Fatalf("the mail-frame block binds %d resize listeners, want 1", got)
	}
	if !strings.Contains(js, "trackMailFrame(frame, place)") {
		t.Fatal("the message body does not track its frame with the shared listener")
	}
	if strings.Contains(js, `window.addEventListener("resize", onResize)`) {
		t.Fatal("a per-message resize listener is still bound")
	}
}
