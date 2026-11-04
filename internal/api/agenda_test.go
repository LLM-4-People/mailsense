package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/agenda"
)

func TestAgendaBoundsShapeAndHorizon(t *testing.T) {
	w := newWorld()
	now := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	w.agenda.res = agenda.Result{Now: now, HorizonHours: 3, Until: now.Add(3 * time.Hour),
		Items: []agenda.Item{{Kind: agenda.KindReply, At: now, Title: "Plan review", ConversationID: "C1",
			MessageID: 7, Slack: 0, Reasons: []agenda.Reason{agenda.ReasonWaitingSince}}}}
	srv, ts, _ := newTestServerWith(t, testOptions(t), w)

	status, _, body := do(t, ts, srv, request{method: "GET", path: "/api/agenda"})
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, body)
	}
	var view AgendaWire
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	if view.HorizonHours != 3 || len(view.Items) != 1 ||
		view.Items[0].Kind != agenda.KindReply || view.Items[0].MessageID != 7 {
		t.Fatalf("view %+v", view)
	}
	if len(w.agenda.horizons) != 1 || w.agenda.horizons[0] != 3*time.Hour {
		t.Fatalf("horizons %v", w.agenda.horizons)
	}

	if status, _, _ := do(t, ts, srv, request{method: "GET", path: "/api/agenda?hours=8"}); status != http.StatusOK {
		t.Fatalf("hours=8 status %d", status)
	}
	if got := w.agenda.horizons[len(w.agenda.horizons)-1]; got != 8*time.Hour {
		t.Fatalf("hours=8 sent %v", got)
	}
	for _, q := range []string{"0", "-1", "169", "abc", "NaN"} {
		if status, _, body := do(t, ts, srv, request{method: "GET", path: "/api/agenda?hours=" + q}); status != http.StatusBadRequest {
			t.Fatalf("hours=%s status %d: %s", q, status, body)
		}
	}
}

func TestAgendaEmptyItemsIsAnArray(t *testing.T) {
	w := newWorld()
	w.agenda.res = agenda.Result{Items: nil}
	srv, ts, _ := newTestServerWith(t, testOptions(t), w)
	_, _, body := do(t, ts, srv, request{method: "GET", path: "/api/agenda"})
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["items"]) != "[]" {
		t.Fatalf("items %s", raw["items"])
	}
}

// The agenda wire keeps only the rows and counts the screen renders. The
// service still builds the bounds, the next-event shortcut and the slack
// arithmetic for its own consumers.
func TestAgendaUsesTheWireProjection(t *testing.T) {
	w := newWorld()
	now := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	last := now.Add(-time.Hour)
	end := now.Add(2 * time.Hour)
	next := agenda.Item{Kind: agenda.KindEvent, At: now.Add(time.Hour), Title: "Next"}
	w.agenda.res = agenda.Result{
		Now: now, HorizonHours: 3, Until: now.Add(3 * time.Hour),
		Items: []agenda.Item{{
			Kind: agenda.KindReply, At: now, LastAt: &last, End: &end,
			Title: "Plan review", Detail: "Waiting since yesterday",
			ConversationID: "C1", MessageID: 7, EventID: "ev-1",
			Location: "Room A", AllDay: true, Participants: 4, Slack: -120,
			LinkedEventID: "ev-1", LinkedEvent: "Kickoff",
			Reasons: []agenda.Reason{agenda.ReasonWaitingSince, agenda.ReasonMeetingWithThem},
		}},
		Events: 1, Obligations: 1, Backlog: 2, Deadlines: 3,
		NextEvent: &next, BusySeconds: 1800, Truncated: true,
	}
	srv, ts, _ := newTestServerWith(t, testOptions(t), w)
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/agenda"})
	mustStatus(t, status, http.StatusOK, data)
	body := decodeMap(t, data)
	for _, path := range []string{"horizon_hours", "items.0.kind", "items.0.at", "items.0.last_at",
		"items.0.title", "items.0.detail", "items.0.message_id", "items.0.location",
		"items.0.all_day", "items.0.linked_event_title", "items.0.reasons",
		"events", "obligations", "backlog", "deadlines", "truncated"} {
		if _, ok := jsonAtPath(body, path); !ok {
			t.Errorf("agenda wire dropped %s", path)
		}
	}
	for _, path := range []string{"now", "until", "next_event", "busy_seconds",
		"items.0.end", "items.0.conversation_id", "items.0.event_id",
		"items.0.participants", "items.0.slack_seconds", "items.0.linked_event_id"} {
		if _, ok := jsonAtPath(body, path); ok {
			t.Errorf("agenda wire still carries %s", path)
		}
	}
}

// TestAgendaHoursFailuresAreOneShape: a malformed horizon and an out-of-range
// one are the same refusal. They used to differ (bare invalid_hours with no
// params versus a ranged one), which forced the client to carry two messages
// for one parameter.
func TestAgendaHoursFailuresAreOneShape(t *testing.T) {
	srv, ts, w := newTestServer(t)
	var rejected *apiErrorPayload
	for _, value := range []string{"abc", "NaN", "Inf", "0", "-1", "", "169", "1000"} {
		t.Run(value, func(t *testing.T) {
			status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/agenda?hours=" + url.QueryEscape(value)})
			mustStatus(t, status, 400, data)
			got := decodeAPIError(t, data)
			if got.Code != "invalid_hours" {
				t.Fatalf("code %q, want invalid_hours: %s", got.Code, data)
			}
			wantParams := map[string]string{
				"name": "hours", "min": "1", "max": "168",
			}
			if len(got.Params) != len(wantParams) {
				t.Fatalf("params %#v, want %#v: %s", got.Params, wantParams, data)
			}
			for key, want := range wantParams {
				if got.Params[key] != want {
					t.Fatalf("params[%q] = %q, want %q: %s", key, got.Params[key], want, data)
				}
			}
			if rejected == nil {
				rejected = &got
				return
			}
			if got.Code != rejected.Code || got.Message != rejected.Message {
				t.Fatalf("horizon refusal shape drifted: %#v vs %#v", got, *rejected)
			}
			for key, want := range rejected.Params {
				if got.Params[key] != want {
					t.Fatalf("horizon refusal params drifted: %#v vs %#v", got.Params, rejected.Params)
				}
			}
		})
	}
	if len(w.agenda.horizons) != 0 {
		t.Fatalf("a refused horizon reached the service: %v", w.agenda.horizons)
	}
}
