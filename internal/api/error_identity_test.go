package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/sidecar"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// apiBody performs one request and decodes the error object the wire nests
// under "error".
func apiBody(t *testing.T, ts *httptest.Server, srv *Server, method, path, body string) (int, map[string]any) {
	t.Helper()
	code, _, data := do(t, ts, srv, request{method: method, path: path, body: body})
	var decoded struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("%s %s: %v (%s)", method, path, err, data)
	}
	if decoded.Error == nil {
		t.Fatalf("%s %s: no error object in %s", method, path, data)
	}
	return code, decoded.Error
}

// TestNotFoundIsOneAnswer: the router's unknown path, an unknown setting path
// and an unknown message all answer the one notFound() error. Each used to
// spell status, code and message itself.
func TestNotFoundIsOneAnswer(t *testing.T) {
	w := newWorld()
	w.messages.err = store.ErrNotFound
	srv, ts, _ := newTestServerWith(t, testOptions(t), w)

	code, unknownPath := apiBody(t, ts, srv, "GET", "/api/does-not-exist", "")
	if code != http.StatusNotFound || unknownPath["code"] != "not_found" || unknownPath["message"] != "not found" {
		t.Fatalf("unknown path: %d %v", code, unknownPath)
	}
	code, unknownSetting := apiBody(t, ts, srv, "GET", "/api/settings/a/b", "")
	if code != http.StatusNotFound {
		t.Fatalf("unknown setting path: %d %v", code, unknownSetting)
	}
	code, unknownMessage := apiBody(t, ts, srv, "GET", "/api/messages/42", "")
	if code != http.StatusNotFound {
		t.Fatalf("unknown message: %d %v", code, unknownMessage)
	}
	for name, body := range map[string]map[string]any{
		"unknown setting path": unknownSetting,
		"unknown message":      unknownMessage,
	} {
		if !reflect.DeepEqual(body, unknownPath) {
			t.Fatalf("%s answers %v, the router answers %v", name, body, unknownPath)
		}
	}
}

// TestShuttingDownIsOneAnswer: a refused handler (middleware), a shutdown
// sentinel from a service and a stopped runtime all answer the one
// shuttingDown() error.
func TestShuttingDownIsOneAnswer(t *testing.T) {
	refusedWorld := newWorld()
	refused, refusedTS, _ := newTestServerWith(t, testOptions(t), refusedWorld)
	refused.handlers.Refuse()
	code, refusedBody := apiBody(t, refusedTS, refused, "GET", "/api/status", "")
	if code != http.StatusServiceUnavailable || refusedBody["code"] != "shutting_down" {
		t.Fatalf("refused handler: %d %v", code, refusedBody)
	}

	mappedWorld := newWorld()
	mappedWorld.status.err = ErrShutdown
	mapped, mappedTS, _ := newTestServerWith(t, testOptions(t), mappedWorld)
	code, mappedBody := apiBody(t, mappedTS, mapped, "GET", "/api/status", "")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("mapped shutdown: %d %v", code, mappedBody)
	}

	runtimeWorld := newWorld()
	runtimeWorld.sidecar.statusErr = sidecar.ErrStopped
	runtime, runtimeTS, _ := newTestServerWith(t, testOptions(t), runtimeWorld)
	code, runtimeBody := apiBody(t, runtimeTS, runtime, "GET", "/api/sidecar", "")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("stopped runtime: %d %v", code, runtimeBody)
	}

	for name, body := range map[string]map[string]any{"mapped shutdown": mappedBody, "stopped runtime": runtimeBody} {
		if !reflect.DeepEqual(body, refusedBody) {
			t.Fatalf("%s answers %v, the middleware answers %v", name, body, refusedBody)
		}
	}
}

// TestSidecarTripleIsValidatedOnce: install and verify take the same
// runtime/model/variant triple, and every missing field answers the same 400 on
// both routes.
func TestSidecarTripleIsValidatedOnce(t *testing.T) {
	srv, ts, _ := newTestServer(t)
	full := map[string]string{"runtime": "llama-server", "model": "m", "variant": "cpu"}
	cases := map[string]map[string]string{"the empty body": {}}
	for _, missing := range []string{"runtime", "model", "variant"} {
		payload := map[string]string{}
		for key, value := range full {
			if key != missing {
				payload[key] = value
			}
		}
		cases["no "+missing] = payload
	}
	for _, route := range []string{"/api/sidecar/install", "/api/sidecar/verify"} {
		var want map[string]any
		for name, payload := range cases {
			body, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			code, got := apiBody(t, ts, srv, "POST", route, string(body))
			if code != http.StatusBadRequest || got["code"] != "invalid_request" {
				t.Fatalf("%s (%s): %d %v", route, name, code, got)
			}
			if want == nil {
				want = got
				continue
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%s (%s) answers %v, another case answers %v", route, name, got, want)
			}
		}
	}
}

// TestJobNotFoundIsOneAnswer: the detail and the cancel route name the same
// missing job the same way.
func TestJobNotFoundIsOneAnswer(t *testing.T) {
	srv, ts, _ := newTestServer(t)
	code, detail := apiBody(t, ts, srv, "GET", "/api/jobs/nope", "")
	if code != http.StatusNotFound || detail["message"] != "no such job" {
		t.Fatalf("job detail: %d %v", code, detail)
	}
	code, cancel := apiBody(t, ts, srv, "POST", "/api/jobs/nope/cancel", "")
	if code != http.StatusNotFound {
		t.Fatalf("job cancel: %d %v", code, cancel)
	}
	if !reflect.DeepEqual(cancel, detail) {
		t.Fatalf("cancel answers %v, detail answers %v", cancel, detail)
	}
}

// TestSharedErrorConstructors: the identities the wiring returns directly (a
// missing mailbox pick, an unreadable triage profile, a failed provider test)
// have one constructor each, and the provider-test one is also what mapError
// answers for engine.ErrTestFailed. The two used to be spelled separately, so
// a message change reached one caller only.
func TestSharedErrorConstructors(t *testing.T) {
	srv := &Server{}
	if got := srv.mapError(engine.ErrTestFailed); !reflect.DeepEqual(got, ProviderTestFailed()) {
		t.Fatalf("mapError(engine.ErrTestFailed) = %+v, constructor = %+v", got, ProviderTestFailed())
	}
	for _, tc := range []struct {
		err     *APIError
		status  int
		code    string
		message string
	}{
		{ProviderTestFailed(), http.StatusBadGateway, "provider_test_failed", "the provider test failed"},
		{ErrNoMailboxPicked(), http.StatusUnprocessableEntity, "no_mailbox_picked", "no mailbox is picked"},
		{ErrInvalidProfile(), http.StatusUnprocessableEntity, "invalid_profile", "the triage profile is invalid"},
	} {
		if tc.err.Status != tc.status || tc.err.Code != tc.code || tc.err.Message != tc.message {
			t.Errorf("%s = %d %q %q", tc.code, tc.err.Status, tc.err.Code, tc.err.Message)
		}
	}
}

// TestInvalidProviderIDKeepsBothRequestShapes: one constructor owns the
// malformed record-id answer. The path segment keeps the API-wide 400
// invalid_id; a record write keeps the 422 invalid_provider the editor's
// validation panel lists.
func TestInvalidProviderIDKeepsBothRequestShapes(t *testing.T) {
	path := InvalidProviderID(true)
	if path.Status != http.StatusBadRequest || path.Code != "invalid_id" || path.Message != "invalid provider id" {
		t.Fatalf("path form = %+v", path)
	}
	record := InvalidProviderID(false)
	if record.Status != http.StatusUnprocessableEntity || record.Code != "invalid_provider" || record.Message != "invalid provider id" {
		t.Fatalf("record form = %+v", record)
	}
}
