package api

import (
	"strings"
	"testing"
)

// TestEveryPathIDRouteRefusesAMalformedID pins one contract across every route
// with a numeric path id: a value that is not a positive integer is the same
// 400 invalid_id. The log entry route used to answer its own 404 log_not_found
// for a malformed sequence, which told a client that a typo in the URL meant
// "this entry rotated away" and forced a second message for one failure. A
// well-formed id that no longer exists is still the route's own 404.
func TestEveryPathIDRouteRefusesAMalformedID(t *testing.T) {
	srv, ts, _ := newTestServer(t)
	routes := []struct {
		name   string
		method string
		path   string
	}{
		{"message", "GET", "/api/messages/%s"},
		{"message attachment", "GET", "/api/messages/%s/attachments/0"},
		{"triage result", "GET", "/api/triage/%s"},
		{"answer history", "GET", "/api/ask/history/%s"},
		{"held removal folder", "POST", "/api/held-removals/%s/confirm"},
		{"log entry", "GET", "/api/logs/%s"},
	}
	for _, route := range routes {
		t.Run(route.name, func(t *testing.T) {
			for _, value := range []string{"abc", "", "0", "-1", "1.5", "99999999999999999999"} {
				path := route.path
				if value == "" {
					// An empty segment does not match the pattern; the route's
					// own fallback answers, which is not this contract.
					continue
				}
				status, _, data := do(t, ts, srv, request{method: route.method,
					path: strings.Replace(path, "%s", value, 1)})
				mustStatus(t, status, 400, data)
				if code := errorCode(t, data); code != "invalid_id" {
					t.Fatalf("%s: code %q, want invalid_id: %s", path, code, data)
				}
			}
		})
	}
}

// TestLogEntryKeepsItsOwnNotFound: a well-formed sequence that is not in the
// retained ring is still the route's 404, distinct from a malformed one.
func TestLogEntryKeepsItsOwnNotFound(t *testing.T) {
	srv, ts, _ := newTestServer(t)
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/logs/999999"})
	mustStatus(t, status, 404, data)
	if code := errorCode(t, data); code != "log_not_found" {
		t.Fatalf("code %q, want log_not_found: %s", code, data)
	}
}
