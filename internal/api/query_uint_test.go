package api

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestQueryUintHasOneOwner: the SSE cursor and the logs cursor read the same
// parser; neither reads its own query values.
func TestQueryUintHasOneOwner(t *testing.T) {
	data, err := os.ReadFile("sse.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	if !strings.Contains(source, `queryUint(r, "since", invalidSince)`) {
		t.Error("sseSince does not read the since cursor through queryUint")
	}
	if strings.Contains(source, `r.URL.Query()["since"]`) {
		t.Error("sseSince still reads the query itself")
	}
	logs, err := os.ReadFile("routes_logs.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logs), `queryUint(r, "since", nil)`) {
		t.Error("the logs route does not read its cursor through queryUint")
	}
}

// TestQueryUintShape: absent, present, empty and malformed values, and the two
// callers' own problem shapes.
func TestQueryUintShape(t *testing.T) {
	for _, c := range []struct {
		name    string
		raw     string
		want    uint64
		present bool
		code    string
	}{
		{"absent", "", 0, false, ""},
		{"zero", "?since=0", 0, true, ""},
		{"value", "?since=42", 42, true, ""},
		{"empty", "?since=", 0, true, "invalid_since"},
		{"malformed", "?since=abc", 0, true, "invalid_since"},
		{"negative", "?since=-1", 0, true, "invalid_since"},
	} {
		got, present, err := queryUint(httptest.NewRequest("GET", "/api/logs"+c.raw, nil), "since", nil)
		if present != c.present || got != c.want {
			t.Errorf("%s: got %d present=%v, want %d present=%v", c.name, got, present, c.want, c.present)
		}
		if c.code == "" {
			if err != nil {
				t.Errorf("%s: err = %v", c.name, err)
			}
			continue
		}
		if err == nil || errorCodeString(err) != c.code {
			t.Errorf("%s: err = %v, want %s", c.name, err, c.code)
		}
	}
	// The SSE cursor names its own problem, so the message is not the generic
	// one the parser would build from the parameter name.
	_, _, err := queryUint(httptest.NewRequest("GET", "/api/events?since=abc", nil), "since", invalidSince)
	if apiErr, ok := err.(*APIError); !ok || apiErr.Message != "invalid since cursor" {
		t.Fatalf("queryUint with the SSE problem = %v, want invalid since cursor", err)
	}
}
