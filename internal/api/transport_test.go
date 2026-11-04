package api

import (
	"strconv"
	"strings"
	"testing"
)

func TestHostAndOrigin(t *testing.T) {
	srv, ts, _ := newTestServer(t)
	u := endpointURL(t, ts)
	port := u.Port()
	cases := []struct {
		name   string
		in     request
		status int
	}{
		{"host ok", request{method: "GET", path: "/api/status"}, 200},
		{"host evil", request{method: "GET", path: "/api/status", host: "evil.test:" + port}, 403},
		{"host localhost", request{method: "GET", path: "/api/status", host: "localhost:" + port}, 403},
		{"host no port", request{method: "GET", path: "/api/status", host: "127.0.0.1"}, 403},
		{"origin evil", request{method: "GET", path: "/api/status", headers: map[string]string{"Origin": "http://evil.test"}}, 403},
		{"origin null", request{method: "GET", path: "/api/status", headers: map[string]string{"Origin": "null"}}, 403},
		{"origin same", request{method: "GET", path: "/api/status", headers: map[string]string{"Origin": "http://" + u.Host}}, 200},
		{"origin absent", request{method: "GET", path: "/api/status"}, 200},
		{"fetch cross-site", request{method: "GET", path: "/api/status", headers: map[string]string{"Sec-Fetch-Site": "cross-site"}}, 403},
		{"fetch same-origin", request{method: "GET", path: "/api/status", headers: map[string]string{"Sec-Fetch-Site": "same-origin"}}, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, _, data := do(t, ts, srv, tc.in)
			mustStatus(t, status, tc.status, data)
			if tc.status == 403 {
				code := errorCode(t, data)
				if code != "forbidden_host" && code != "forbidden_origin" {
					t.Fatalf("code %q", code)
				}
			}
		})
	}
}

func TestJSONBoundaries(t *testing.T) {
	srv, ts, _ := newTestServer(t)
	big := `{"zone":"` + strings.Repeat("a", 2048) + `"}`
	cases := []struct {
		name   string
		in     request
		status int
		code   string
	}{
		{"body too large", request{method: "POST", path: "/api/learning/zone", body: big}, 413, "payload_too_large"},
		{"wrong content type", request{method: "POST", path: "/api/learning/zone", body: `{"zone":"UTC"}`, headers: map[string]string{"Content-Type": "text/plain"}}, 415, "unsupported_media_type"},
		{"trailing data", request{method: "POST", path: "/api/learning/zone", body: `{"zone":"UTC"} garbage`}, 400, "invalid_json"},
		{"unknown field", request{method: "POST", path: "/api/learning/zone", body: `{"nope":1}`}, 400, "invalid_json"},
		{"truncated", request{method: "POST", path: "/api/learning/zone", body: `{"zone":"UTC"`}, 400, "invalid_json"},
		{"empty required body", request{method: "POST", path: "/api/learning/zone"}, 400, "invalid_json"},
		{"settings missing value", request{method: "PUT", path: "/api/settings/time.zone", body: `{}`}, 400, "invalid_request"},
		{"settings null value", request{method: "PUT", path: "/api/settings/time.zone", body: `{"value":null}`}, 400, "invalid_request"},
		{"since negative", request{method: "GET", path: "/api/logs?since=-1"}, 400, "invalid_since"},
		{"since text", request{method: "GET", path: "/api/logs?since=abc"}, 400, "invalid_since"},
		{"limit zero", request{method: "GET", path: "/api/logs?limit=0"}, 400, "invalid_limit"},
		{"limit above max", request{method: "GET", path: "/api/logs?limit=5001"}, 400, "invalid_limit"},
		{"attrs unknown", request{method: "GET", path: "/api/logs?attrs=sometimes"}, 400, "invalid_attrs"},
		{"wrong method", request{method: "DELETE", path: "/api/status"}, 405, "method_not_allowed"},
		{"unknown api with token", request{method: "GET", path: "/api/nope"}, 404, "not_found"},
		{"api root", request{method: "GET", path: "/api"}, 404, "not_found"},
		{"unknown api without token", noAuth(request{method: "GET", path: "/api/nope"}), 401, "unauthorized"},
		{"unknown static", request{method: "GET", path: "/nope"}, 404, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, header, data := do(t, ts, srv, tc.in)
			mustStatus(t, status, tc.status, data)
			if tc.code != "" {
				if got := errorCode(t, data); got != tc.code {
					t.Fatalf("code %q, want %q: %s", got, tc.code, data)
				}
			}
			if tc.status == 405 && header.Get("Allow") == "" {
				t.Fatal("405 without Allow")
			}
			if tc.status >= 400 && tc.status != 404 || (tc.status == 404 && tc.code != "") {
				if header.Get("Content-Type") != "application/json; charset=utf-8" {
					t.Fatalf("content type %q", header.Get("Content-Type"))
				}
			}
		})
	}
}

func TestNoCORSOrCookies(t *testing.T) {
	srv, ts, _ := newTestServer(t)
	cases := []struct {
		name    string
		in      request
		status  int
		headers []string
	}{
		{"api", request{method: "GET", path: "/api/status"}, 200,
			[]string{"X-Content-Type-Options", "Referrer-Policy", "X-Frame-Options", "Cross-Origin-Opener-Policy", "Cross-Origin-Resource-Policy"}},
		{"preflight", request{method: "OPTIONS", path: "/api/status", headers: map[string]string{"Origin": "http://" + endpointURL(t, ts).Host}}, 405, nil},
		{"shell", noAuth(request{method: "GET", path: "/"}), 200,
			[]string{"X-Content-Type-Options", "Referrer-Policy", "X-Frame-Options", "Cross-Origin-Opener-Policy", "Cross-Origin-Resource-Policy", "Content-Security-Policy"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, header, data := do(t, ts, srv, tc.in)
			mustStatus(t, status, tc.status, data)
			if v := header.Get("Access-Control-Allow-Origin"); v != "" {
				t.Fatalf("CORS header %q", v)
			}
			if v := header.Get("Access-Control-Allow-Credentials"); v != "" {
				t.Fatalf("CORS credentials %q", v)
			}
			if cookies := header.Values("Set-Cookie"); len(cookies) != 0 {
				t.Fatalf("cookies %v", cookies)
			}
			for _, name := range tc.headers {
				if header.Get(name) == "" {
					t.Fatalf("missing %s", name)
				}
			}
		})
	}
}

func TestStaticShell(t *testing.T) {
	srv, ts, _ := newTestServer(t)
	status, header, data := do(t, ts, srv, noAuth(request{method: "GET", path: "/"}))
	mustStatus(t, status, 200, data)
	csp := header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "frame-src 'self'", "img-src 'self' data: blob:", "style-src 'self' blob:", "script-src 'self'"} {
		if !strings.Contains(csp, want) {
			t.Fatalf("CSP lacks %q: %q", want, csp)
		}
	}
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
		t.Fatalf("CSP weakens inline execution: %q", csp)
	}
	if !strings.Contains(string(data), "Mailsense") {
		t.Fatalf("placeholder body: %s", data)
	}
	status, header, data = do(t, ts, srv, noAuth(request{method: "GET", path: "/index.html"}))
	mustStatus(t, status, 308, data)
	if header.Get("Location") != "/" {
		t.Fatalf("Location %q", header.Get("Location"))
	}
}

// TestLoopbackAuthorityIsOneRule pins the shared Host/Origin rule: the same
// authority list must be accepted or refused identically by both checks, so a
// drift in one can never open the other. The two checks are the DNS-rebinding
// and cross-site defences.
func TestLoopbackAuthorityIsOneRule(t *testing.T) {
	srv, ts, _ := newTestServer(t)
	port := endpointURL(t, ts).Port()
	// The httptest fixture never announces a port, so set the one the server
	// bound: without it `boundPort()==0` accepts any loopback port and the
	// port rule under test would be a no-op.
	bound, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	srv.port = bound
	srv.mu.Unlock()
	cases := []struct {
		authority string
		want      bool
	}{
		{"127.0.0.1:" + port, true},
		{"127.0.0.1:1", false},       // another port
		{"127.0.0.1:0", false},       // out of range
		{"127.0.0.1:65536", false},   // out of range
		{"127.0.0.1", false},         // no port
		{"[::1]:" + port, false},     // not the v4 loopback
		{"localhost:" + port, false}, // a name, not the address
		{"127.0.0.1:" + port + "x", false},
	}
	for _, tc := range cases {
		t.Run(tc.authority, func(t *testing.T) {
			if got := srv.loopbackAuthority(tc.authority); got != tc.want {
				t.Fatalf("loopbackAuthority(%q) = %v, want %v", tc.authority, got, tc.want)
			}
			// The two callers must agree with the shared rule.
			hostReq := request{method: "GET", path: "/api/status", host: tc.authority}
			originReq := request{method: "GET", path: "/api/status",
				headers: map[string]string{"Origin": "http://" + tc.authority}}
			hostStatus, _, hostBody := do(t, ts, srv, hostReq)
			originStatus, _, originBody := do(t, ts, srv, originReq)
			wantStatus := 403
			if tc.want {
				wantStatus = 200
			}
			mustStatus(t, hostStatus, wantStatus, hostBody)
			mustStatus(t, originStatus, wantStatus, originBody)
		})
	}
}
