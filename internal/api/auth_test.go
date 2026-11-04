package api

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAuth(t *testing.T) {
	srv, ts, _ := newTestServer(t)
	valid := srv.token
	sameLength := strings.Repeat("a", len(valid))
	cases := []struct {
		name   string
		in     request
		status int
	}{
		{"missing", noAuth(request{method: "GET", path: "/api/status"}), 401},
		{"wrong same length", noAuth(request{method: "GET", path: "/api/status", headers: map[string]string{"Authorization": "Bearer " + sameLength}}), 401},
		{"wrong other length", noAuth(request{method: "GET", path: "/api/status", headers: map[string]string{"Authorization": "Bearer x"}}), 401},
		{"basic", noAuth(request{method: "GET", path: "/api/status", headers: map[string]string{"Authorization": "Basic " + valid}}), 401},
		{"empty bearer", noAuth(request{method: "GET", path: "/api/status", headers: map[string]string{"Authorization": "Bearer "}}), 401},
		{"valid", request{method: "GET", path: "/api/status"}, 200},
		{"query token on status", noAuth(request{method: "GET", path: "/api/status?token=" + valid}), 401},
		{"both equal", request{method: "GET", path: "/api/status?token=" + valid}, 200},
		{"query ignored on status", request{method: "GET", path: "/api/status?token=" + sameLength}, 200},
		{"double slash no token", noAuth(request{method: "GET", path: "//api/status"}), 401},
		{"dot segment no token", noAuth(request{method: "GET", path: "/x/../api/status"}), 401},
		{"double slash with token", request{method: "GET", path: "//api/status"}, 307},
		{"token in body", noAuth(request{method: "POST", path: "/api/learning/zone", body: `{"zone":"` + valid + `"}`}), 401},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, header, data := do(t, ts, srv, tc.in)
			mustStatus(t, status, tc.status, data)
			if tc.status == 401 {
				if header.Get("WWW-Authenticate") != "Bearer" {
					t.Fatalf("WWW-Authenticate = %q", header.Get("WWW-Authenticate"))
				}
				if code := errorCode(t, data); code != "unauthorized" {
					t.Fatalf("code %q", code)
				}
			}
		})
	}
	if _, _, data := do(t, ts, srv, noAuth(request{method: "GET", path: "/api/status"})); strings.Contains(string(data), valid) {
		t.Fatal("the 401 body contains the token")
	}
}

func TestSSEQueryToken(t *testing.T) {
	srv, ts, _ := newTestServer(t)
	for _, tc := range []struct {
		name string
		path string
		auth bool
		want int
	}{
		{"query", "/api/events?token=" + srv.token, false, 200},
		{"header", "/api/events", true, 200},
		{"both equal", "/api/events?token=" + srv.token, true, 200},
		{"both different", "/api/events?token=" + strings.Repeat("x", len(srv.token)), true, 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, br, cancel := streamRequest(t, ts, srv, tc.path, tc.auth, context.Background())
			defer cancel()
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				data := make([]byte, 512)
				n, _ := resp.Body.Read(data)
				t.Fatalf("status %d, want %d: %s", resp.StatusCode, tc.want, data[:n])
			}
			if tc.want == 200 {
				line, err := br.ReadString('\n')
				if err != nil {
					t.Fatal(err)
				}
				if !strings.HasPrefix(line, "retry:") {
					t.Fatalf("first SSE line %q", line)
				}
			}
		})
	}
}

func TestSSEFirstFramesDoNotContainToken(t *testing.T) {
	srv, ts, _ := newTestServer(t)
	ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	resp, br, cancel := streamRequest(t, ts, srv, "/api/events", true, ctx)
	defer cancel()
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	buf := make([]byte, 0, 4096)
	// retry frame + one heartbeat is enough to cover headers and framing.
	for len(buf) < 32 {
		line, err := br.ReadString('\n')
		if err != nil {
			break
		}
		buf = append(buf, line...)
	}
	if strings.Contains(string(buf), srv.token) {
		t.Fatal("the first SSE frames contain the token")
	}
	if strings.Contains(resp.Header.Get("Content-Type"), srv.token) {
		t.Fatal("the SSE content type contains the token")
	}
}
