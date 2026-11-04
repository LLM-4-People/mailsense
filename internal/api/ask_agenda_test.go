package api

import (
	"net/http"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/qa"
)

// Ask embeds the same agenda result as Home. The HTTP response must carry the
// projected agenda, not the service value, and the history detail read must
// use the same projection.
func TestAskAgendaUsesTheWireProjection(t *testing.T) {
	srv, ts, w := newTestServer(t)
	result := agendaWireFixture()
	view := qa.AskView{
		ID: 7, Question: "What is next?", Mode: qa.ModeComputed,
		Agenda: &result,
	}
	w.ask.view = view
	w.ask.historyItem = view
	w.ask.historyItemOK = true

	check := func(t *testing.T, data []byte) {
		t.Helper()
		body := decodeMap(t, data)
		agendaBody, ok := body["agenda"].(map[string]any)
		if !ok {
			t.Fatalf("agenda = %#v", body["agenda"])
		}
		for _, path := range []string{"horizon_hours", "items.0.title", "items.0.message_id",
			"items.0.linked_event_title", "items.0.reasons.0", "events", "obligations",
			"backlog", "deadlines", "truncated"} {
			if _, ok := jsonAtPath(agendaBody, path); !ok {
				t.Errorf("Ask agenda dropped %s", path)
			}
		}
		for _, path := range []string{"now", "until", "next_event", "busy_seconds",
			"items.0.slack_seconds", "items.0.conversation_id",
			"items.0.event_id", "items.0.participants", "items.0.linked_event_id"} {
			if _, ok := jsonAtPath(agendaBody, path); ok {
				t.Errorf("Ask agenda still carries %s", path)
			}
		}
		if body["question"] != "What is next?" || body["mode"] != qa.ModeComputed {
			t.Errorf("Ask answer fields changed: %#v / %#v", body["question"], body["mode"])
		}
	}

	status, _, data := do(t, ts, srv, request{method: "POST", path: "/api/ask",
		body: `{"question":"What is next?","language":"en"}`})
	mustStatus(t, status, http.StatusOK, data)
	check(t, data)

	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/ask/history/7"})
	mustStatus(t, status, http.StatusOK, data)
	check(t, data)
}
