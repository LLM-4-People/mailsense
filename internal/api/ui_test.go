package api

import (
	"errors"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/engine"
)

// TestRootServesTheEmbeddedUI is the headless check that the static route is
// the real shell, not the phase-4 placeholder, and that it stays
// unauthenticated while carrying no launch data.
func TestRootServesTheEmbeddedUI(t *testing.T) {
	srv, ts, _ := newTestServer(t)
	status, header, data := do(t, ts, srv, noAuth(request{method: "GET", path: "/"}))
	mustStatus(t, status, 200, data)
	body := string(data)
	if !strings.Contains(body, `id="app"`) || !strings.Contains(body, `href="/app.css?v=`) || !strings.Contains(body, `src="/app.js?v=`) {
		t.Fatalf("root is not the embedded shell: %s", body)
	}
	if strings.Contains(body, "{{asset") {
		t.Fatalf("root carries an unresolved asset placeholder: %s", body)
	}
	if strings.Contains(body, srv.token) || strings.Contains(body, "token=") {
		t.Fatal("the shell contains launch data")
	}
	if !strings.Contains(header.Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatal("no CSP on the shell")
	}

	status, header, data = do(t, ts, srv, noAuth(request{method: "GET", path: "/app.css"}))
	mustStatus(t, status, 200, data)
	if got := header.Get("Content-Type"); got != "text/css; charset=utf-8" {
		t.Fatalf("style content type %q", got)
	}
	status, _, data = do(t, ts, srv, noAuth(request{method: "GET", path: "/app.js"}))
	mustStatus(t, status, 200, data)
	if !strings.Contains(string(data), "Authorization") {
		t.Fatal("the script is not the app")
	}
	status, _, _ = do(t, ts, srv, noAuth(request{method: "GET", path: "/missing"}))
	mustStatus(t, status, 404, nil)
}

func TestQuestionsArePreGateAndContentFree(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.learning.allowErr = errors.New("learning is locked")
	w.triage.questions = QuestionsView{
		Name:        "test profile",
		Fingerprint: "fingerprint",
		Questions: []engine.Question{{
			Name:         "category",
			Type:         engine.Choice,
			Instructions: "Pick the category.",
			Options:      []engine.Option{{Name: "work"}, {Name: "personal"}},
		}},
	}
	before := w.learning.calls
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/questions"})
	mustStatus(t, status, 200, data)
	if w.learning.calls != before {
		t.Fatal("the pre-gate question read consulted the learning gate")
	}
	body := decodeMap(t, data)
	questions, _ := body["questions"].([]any)
	if len(questions) != 1 {
		t.Fatalf("questions: %v", body["questions"])
	}
	if w.triage.questionCalls != 1 {
		t.Fatalf("question calls %d", w.triage.questionCalls)
	}
}
