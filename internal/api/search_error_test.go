package api

import (
	"net/http"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/search"
)

// TestQueryTooLongKeepsTheConfiguredMaximum: the client can name the limit it
// exceeded instead of showing a generic validation failure.
func TestQueryTooLongKeepsTheConfiguredMaximum(t *testing.T) {
	srv, _, _ := newTestServer(t)
	mapped := srv.mapError(&search.QueryTooLongError{Max: 512})
	if mapped.Status != http.StatusUnprocessableEntity || mapped.Code != "query_too_long" {
		t.Fatalf("mapped %d %s", mapped.Status, mapped.Code)
	}
	if mapped.Params["max"] != "512" {
		t.Fatalf("params = %+v", mapped.Params)
	}
}
