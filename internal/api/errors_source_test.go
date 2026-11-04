package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/source"
)

// TestSourceUnavailableKeepsTheActionableCause: a discovery or sync refusal
// carries the connector's named next step as a parameter; the generic message
// stays for a source with no cause.
func TestSourceUnavailableKeepsTheActionableCause(t *testing.T) {
	srv := &Server{}
	named := srv.mapError(source.UnavailableCause("outlook_not_running",
		"outlook: classic Outlook is not running. Start Outlook"))
	if named.Status != http.StatusServiceUnavailable || named.Code != "source_unavailable" {
		t.Fatalf("status %d code %q", named.Status, named.Code)
	}
	if got := named.Params["reason"]; !strings.Contains(got, "Start Outlook") {
		t.Fatalf("reason = %q", got)
	}
	if got := named.Params["cause"]; got != "outlook_not_running" {
		t.Fatalf("cause = %q", got)
	}
	generic := srv.mapError(source.ErrUnavailable)
	if generic.Params != nil {
		t.Fatalf("a source without a cause got params %+v", generic.Params)
	}
}
