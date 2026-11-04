package source

import (
	"errors"
	"fmt"
	"testing"
)

// TestCauseReturnsTheConnectorReason: the API can offer the connector's named
// next step instead of a generic unavailability, and a wrapped or unrelated
// error never invents one.
func TestCauseReturnsTheConnectorReason(t *testing.T) {
	named := UnavailableCause("outlook_not_running", "outlook: classic Outlook is not running. Start Outlook")
	if code, msg := Cause(named); code != "outlook_not_running" || msg != "outlook: classic Outlook is not running. Start Outlook" {
		t.Fatalf("cause = %q, %q", code, msg)
	}
	if code, msg := Cause(fmt.Errorf("discovery: %w", named)); code != "outlook_not_running" || msg == "" {
		t.Fatalf("a wrapped source error lost its cause: %q %q", code, msg)
	}
	if code, msg := Cause(errors.New("other")); code != "" || msg != "" {
		t.Fatalf("an unrelated error produced a cause: %q %q", code, msg)
	}
	if code, msg := Cause(Unavailable("plain")); code != "" || msg != "plain" {
		t.Fatalf("a causeless unavailability = %q %q", code, msg)
	}
	if !errors.Is(named, ErrUnavailable) {
		t.Fatal("the named error must keep matching ErrUnavailable")
	}
}
