package api

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/conversations"
	"github.com/LLM-4-People/mailsense/internal/layer1"
	"github.com/LLM-4-People/mailsense/internal/priority"
	"github.com/LLM-4-People/mailsense/internal/qa"
	"github.com/LLM-4-People/mailsense/internal/search"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/triage"
	worldsvc "github.com/LLM-4-People/mailsense/internal/world"
)

type apiErrorPayload struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Params  map[string]string `json:"params"`
}

type apiErrorEnvelope struct {
	Error apiErrorPayload `json:"error"`
}

func decodeAPIError(t *testing.T, data []byte) apiErrorPayload {
	t.Helper()
	var envelope apiErrorEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("not a JSON error envelope: %v: %s", err, data)
	}
	return envelope.Error
}

// pageLimitURL appends one limit value to a route path, keeping an existing
// query string intact.
func pageLimitURL(path, value string) string {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return path + separator + "limit=" + url.QueryEscape(value)
}

// TestListQueryBoundsAreOneContract drives every list route through the same
// limit table: absent/default, min, max, zero, negative, above max and
// malformed. Accepted values must reach the service unchanged; every rejected
// value must carry the same invalid_limit code, message and name/min/max
// params, whether it failed to parse or fell outside the route's range.
func TestListQueryBoundsAreOneContract(t *testing.T) {
	srv, ts, w := newTestServer(t)
	routes := []struct {
		name     string
		path     string
		min      int
		max      int
		def      int
		observed func(*world) int
	}{
		{"triage", "/api/triage", 1, triage.TriagePageMax, triage.TriagePageDefault,
			func(w *world) int { return w.triage.lastPage.Limit }},
		{"priority", "/api/priority", 1, priority.MaxLimit, priority.DefaultLimit,
			func(w *world) int { return w.priority.lastLimit }},
		{"conversations", "/api/conversations", conversations.MinLimit, conversations.MaxLimit, 0,
			func(w *world) int { return w.conversations.lastQuery.Limit }},
		{"messages", "/api/messages", 1, store.MessageListMaxLimit, 0,
			func(w *world) int { return w.messages.lastList.Limit }},
		{"search", "/api/search?q=x", 1, MaxSearchAPILimit, 0,
			func(w *world) int { return w.search.query.Limit }},
		{"threads", "/api/threads/C1", 1, store.MaxThreadMessages, threadLimitDefault,
			func(w *world) int { return w.threads.lastLimit }},
		{"labels", "/api/labels/queue?question=category", 0, layer1.LabelQueueMax, 0,
			func(w *world) int { return w.labels.lastQuery.Limit }},
		{"hub", "/api/answers", 1, store.AnswerMaxLimit, store.AnswerDefaultLimit,
			func(w *world) int { return w.hub.lastQuery.Limit }},
		{"logs", "/api/logs", 1, maxLogsPage, defaultLogsLimit,
			func(w *world) int { return w.logs.lastQuery.Limit }},
		{"companies", "/api/companies", worldsvc.MinCompanyLimit, worldsvc.MaxCompanyLimit, worldsvc.DefaultCompanyLimit,
			func(w *world) int { return w.companies.lastLimit }},
		{"ask history", "/api/ask/history", 1, qa.MaxHistorySize, qa.DefaultHistorySize,
			func(w *world) int { return w.ask.lastLimit }},
	}

	for _, route := range routes {
		t.Run(route.name, func(t *testing.T) {
			status, _, data := do(t, ts, srv, request{method: "GET", path: route.path})
			mustStatus(t, status, 200, data)
			if got := route.observed(w); got != route.def {
				t.Fatalf("absent limit reached the service as %d, want the route default %d", got, route.def)
			}

			values := []struct {
				name     string
				value    string
				accepted bool
			}{
				{"min", strconv.Itoa(route.min), true},
				{"max", strconv.Itoa(route.max), true},
				{"zero", "0", route.min <= 0},
				{"negative", "-1", false},
				{"above max", strconv.Itoa(route.max + 1), false},
				{"malformed", "abc", false},
			}
			if route.def >= route.min && route.def <= route.max {
				values = append(values, struct {
					name     string
					value    string
					accepted bool
				}{"default", strconv.Itoa(route.def), true})
			}

			var rejected *apiErrorPayload
			for _, tc := range values {
				t.Run(tc.name, func(t *testing.T) {
					status, _, data := do(t, ts, srv, request{method: "GET", path: pageLimitURL(route.path, tc.value)})
					if tc.accepted {
						mustStatus(t, status, 200, data)
						want, err := strconv.Atoi(tc.value)
						if err != nil {
							t.Fatal(err)
						}
						if got := route.observed(w); got != want {
							t.Fatalf("limit %d reached the service as %d", want, got)
						}
						return
					}
					mustStatus(t, status, 400, data)
					got := decodeAPIError(t, data)
					if got.Code != "invalid_limit" {
						t.Fatalf("code %q, want invalid_limit: %s", got.Code, data)
					}
					wantParams := map[string]string{
						"name": "limit",
						"min":  strconv.Itoa(route.min),
						"max":  strconv.Itoa(route.max),
					}
					if len(got.Params) != len(wantParams) {
						t.Fatalf("params %#v, want %#v", got.Params, wantParams)
					}
					for key, want := range wantParams {
						if got.Params[key] != want {
							t.Fatalf("params[%q] = %q, want %q: %s", key, got.Params[key], want, data)
						}
					}
					if got.Message == "" {
						t.Fatal("rejected limit carries no message")
					}
					if rejected == nil {
						rejected = &got
						return
					}
					if got.Code != rejected.Code || got.Message != rejected.Message {
						t.Fatalf("rejected limit shape drifted: %#v vs %#v", got, *rejected)
					}
					for key, want := range rejected.Params {
						if got.Params[key] != want {
							t.Fatalf("rejected limit params drifted: %#v vs %#v", got.Params, rejected.Params)
						}
					}
				})
			}
		})
	}
}

// TestOffsetErrorsAreOneShape drives every offset route with a negative and a
// malformed offset: both are the same invalid_offset error with params.min=0,
// plus the route's max when the route bounds the offset.
func TestOffsetErrorsAreOneShape(t *testing.T) {
	srv, ts, _ := newTestServer(t)
	routes := []struct {
		name   string
		path   string
		max    string
		values []string
	}{
		{"triage", "/api/triage?offset=%s", "", []string{"-1", "abc"}},
		{"priority", "/api/priority?offset=%s", "", []string{"-1", "abc"}},
		{"search", "/api/search?q=x&offset=%s", strconv.Itoa(search.MaxOffset),
			[]string{"-1", "abc", strconv.Itoa(search.MaxOffset + 1)}},
	}

	for _, route := range routes {
		t.Run(route.name, func(t *testing.T) {
			var rejected *apiErrorPayload
			for _, value := range route.values {
				t.Run(value, func(t *testing.T) {
					path := strings.Replace(route.path, "%s", url.QueryEscape(value), 1)
					status, _, data := do(t, ts, srv, request{method: "GET", path: path})
					mustStatus(t, status, 400, data)
					got := decodeAPIError(t, data)
					if got.Code != "invalid_offset" {
						t.Fatalf("code %q, want invalid_offset: %s", got.Code, data)
					}
					if got.Params["name"] != "offset" || got.Params["min"] != "0" {
						t.Fatalf("params %#v, want name=offset min=0: %s", got.Params, data)
					}
					if route.max == "" {
						if _, present := got.Params["max"]; present {
							t.Fatalf("unbounded offset advertises a max: %#v", got.Params)
						}
					} else if got.Params["max"] != route.max {
						t.Fatalf("params.max = %q, want %q: %s", got.Params["max"], route.max, data)
					}
					if got.Message == "" {
						t.Fatal("rejected offset carries no message")
					}
					if rejected == nil {
						rejected = &got
						return
					}
					if got.Code != rejected.Code || got.Message != rejected.Message {
						t.Fatalf("rejected offset shape drifted: %#v vs %#v", got, *rejected)
					}
					for key, want := range rejected.Params {
						if got.Params[key] != want {
							t.Fatalf("rejected offset params drifted: %#v vs %#v", got.Params, rejected.Params)
						}
					}
				})
			}
		})
	}
}

// TestMalformedCursorsAnswerOneShape: the two cursor types have different
// owners (the store parses the mail keyset cursor, the conversations service
// parses its query-bound cursor), but a client must not be able to tell. Both
// routes answer the same invalid_cursor payload, whether the handler caught
// the empty value or the owner rejected the malformed one.
func TestMalformedCursorsAnswerOneShape(t *testing.T) {
	srv, ts, w := newTestServer(t)
	// The conversations fake stands in for the service's own rejection; the
	// real service validates a malformed cursor on both page paths even when
	// the result is empty (internal/conversations/page_paths_test.go).
	w.conversations.err = conversations.ErrInvalidCursor
	var rejected *apiErrorPayload
	for _, path := range []string{
		"/api/messages?before=not-a-cursor",
		"/api/messages?before=",
		"/api/conversations?before=not-a-cursor",
		"/api/conversations?before=",
	} {
		t.Run(path, func(t *testing.T) {
			status, _, data := do(t, ts, srv, request{method: "GET", path: path})
			mustStatus(t, status, 400, data)
			got := decodeAPIError(t, data)
			if got.Code != "invalid_cursor" {
				t.Fatalf("code %q, want invalid_cursor: %s", got.Code, data)
			}
			if len(got.Params) != 0 {
				t.Fatalf("cursor params %#v, want none: %s", got.Params, data)
			}
			if rejected == nil {
				rejected = &got
				return
			}
			if got.Code != rejected.Code || got.Message != rejected.Message || len(got.Params) != len(rejected.Params) {
				t.Fatalf("cursor shape drifted: %#v vs %#v", got, *rejected)
			}
		})
	}
}
