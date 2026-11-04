//go:build windows

package desktop

import "testing"

func TestWindowContract(t *testing.T) {
	if WindowTitle != "Mailsense" {
		t.Fatalf("title %q", WindowTitle)
	}
	if WindowWidth != 1280 || WindowHeight != 840 {
		t.Fatalf("size %dx%d", WindowWidth, WindowHeight)
	}
	if got, err := localURL("http://127.0.0.1:54321/#token=abc"); err != nil || got == "" {
		t.Fatalf("bootstrap URL: %q, %v", got, err)
	}
}
