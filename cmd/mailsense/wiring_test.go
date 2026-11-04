package main

import (
	"os"
	"strings"
	"testing"
)

// Label writes arrive one per judged message. They must use the same burst
// coalescer as job progress, never the per-event full status rebuild.
func TestLabelInvalidationIsCoalesced(t *testing.T) {
	body, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "learning.RefreshOnProgress(m, bus, feedback.TopicLabel, buffer, learning.ProgressRefreshInterval)") {
		t.Error("label events rebuild the learning status once per message")
	}
	if strings.Contains(text, "learning.RefreshOn(m, bus, feedback.TopicLabel, buffer)") {
		t.Error("label events still use the per-event learning refresh")
	}
}
