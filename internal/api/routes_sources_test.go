package api

import (
	"net/http/httptest"
	"testing"
)

// Every source action returns the one action view, and the route names the
// source and action it dispatched.
func TestSourceActionsReturnTheActionView(t *testing.T) {
	srv, ts, w := newTestServer(t)
	cases := []struct {
		path   string
		action string
		view   SourceActionView
		calls  func() int
	}{
		{"/api/sources/outlook/start", "start", SourceActionView{Action: "start", Source: "outlook"}, func() int { return w.sources.startCalls }},
		{"/api/sources/outlook/resume", "resume", SourceActionView{Action: "resume", Source: "outlook"}, func() int { return w.sources.resumeCalls }},
		{"/api/sources/outlook/retry-details", "retry-details", SourceActionView{Action: "retry-details", Source: "outlook", Retried: 2}, func() int { return w.sources.retryCalls }},
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			status, _, data := do(t, ts, srv, request{method: "POST", path: tc.path, body: `{}`})
			mustStatus(t, status, 200, data)
			if tc.calls() != 1 {
				t.Fatalf("%s service calls %d", tc.action, tc.calls())
			}
			if w.sources.lastID != "outlook" {
				t.Fatalf("source id %q", w.sources.lastID)
			}
			body := decodeMap(t, data)
			if body["action"] != tc.view.Action || body["source"] != tc.view.Source {
				t.Fatalf("view %#v, want %+v", body, tc.view)
			}
			if tc.view.Retried != 0 && body["retried"] != float64(tc.view.Retried) {
				t.Fatalf("retried %#v", body["retried"])
			}
		})
	}
}

// A missing source is a missing resource, and a connector that does not
// implement the optional capability is a named conflict, never a panic or a
// substitute sync.
func TestSourceActionErrors(t *testing.T) {
	srv, ts, w := newTestServer(t)
	cases := []struct {
		name   string
		path   string
		inject func()
		status int
		code   string
	}{
		{"unknown source", "/api/sources/missing/start", func() { w.sources.startErr = UnknownSource() }, 404, "unknown_source"},
		{"unsupported start", "/api/sources/archive/start", func() {
			w.sources.startErr = SourceActionUnsupported("start", "archive")
		}, 409, "action_unsupported"},
		{"unsupported resume", "/api/sources/archive/resume", func() {
			w.sources.resumeErr = SourceActionUnsupported("resume", "archive")
		}, 409, "action_unsupported"},
		{"unsupported retry", "/api/sources/archive/retry-details", func() {
			w.sources.retryErr = SourceActionUnsupported("retry-details", "archive")
		}, 409, "action_unsupported"},
		{"unknown action", "/api/sources/outlook/reconnect", func() {}, 404, "not_found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w.sources.startErr, w.sources.resumeErr, w.sources.retryErr = nil, nil, nil
			tc.inject()
			status, _, data := do(t, ts, srv, request{method: "POST", path: tc.path, body: `{}`})
			mustStatus(t, status, tc.status, data)
			if code := errorCode(t, data); code != tc.code {
				t.Fatalf("code %q, want %q: %s", code, tc.code, data)
			}
		})
	}
}

// An empty source id is a missing resource, not a handler panic.
func TestSourceActionEmptyID(t *testing.T) {
	srv, _, _ := newTestServer(t)
	r := httptest.NewRequest("POST", "/api/sources//start", nil)
	r.SetPathValue("id", "")
	r.SetPathValue("action", "start")
	rec := httptest.NewRecorder()
	srv.handleSourceAction(rec, r)
	if rec.Code != 404 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if code := errorCode(t, rec.Body.Bytes()); code != "unknown_source" {
		t.Fatalf("code %q", code)
	}
}
