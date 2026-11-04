package api

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/conversations"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
)

func conversationState(id string, st threadintel.State) threadintel.Assessment {
	return threadintel.Assessment{ConversationID: id, State: st, Reason: threadintel.ReasonInboundLast,
		ActorName: "Alice", OwnerActorName: "Alice", LastAt: "2026-11-05T09:00:00Z", LastMessageID: 42, ReplyToMessageID: 41,
		Visibility: threadintel.Complete, Confidence: 0.85}
}

func TestConversationPageShape(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.conversations.page = conversations.Page{
		GeneratedAt: "2026-10-26T10:00:00Z",
		Conversations: []conversations.Row{{
			ConversationID: "C1", Subject: "Budget", SenderName: "Alice Martin", SenderAddress: "alice@atelier-nord.test",
			LastMessageID: 42, LastAt: "2026-11-05T09:00:00Z", FirstAt: "2026-11-01T09:00:00Z",
			Messages: 3, Sent: 1, Received: 2, Unread: 1,
			LastSent: false, LastIsRead: true, HasAttachments: true,
			Snippet: "hello", DueDate: "2026-11-10",
			Participants: []store.ThreadParticipant{{Name: "Alice", Address: "alice@atelier-nord.test",
				Messages: 2, LastAt: "2026-11-05T09:00:00Z"}},
			ParticipantCount: 1,
			Organization:     &conversations.Organization{Key: "c:atelier nord", Label: "Atelier Nord", Declared: true},
			State:            conversationState("C1", threadintel.AwaitingOwner),
			StateSource:      "priority",
		}},
		Total:      1,
		Counts:     map[string]int{"awaiting_owner": 1, "awaiting_other": 0, "answered_by_owner": 0, "answered_by_other": 0, "no_action": 0, "unknown": 0},
		NextBefore: "next-cursor",
		HasMore:    true,
		Preview:    true,
		Coverage:   store.ConversationCoverage{Messages: 3, Conversations: 1, Unthreaded: 0},
	}
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/conversations?limit=7&state=awaiting_owner&q=Budget&before=abc"})
	mustStatus(t, status, 200, data)
	body := decodeMap(t, data)

	if body["generated_at"] != "2026-10-26T10:00:00Z" || body["total"].(float64) != 1 ||
		body["next_before"] != "next-cursor" || body["has_more"] != true || body["preview"] != true {
		t.Fatalf("page header %#v", body)
	}
	convs, ok := body["conversations"].([]any)
	if !ok || len(convs) != 1 {
		t.Fatalf("conversations %#v", body["conversations"])
	}
	row := convs[0].(map[string]any)
	for key, want := range map[string]any{
		"conversation_id": "C1", "subject": "Budget", "sender_name": "Alice Martin",
		"sender_address": "alice@atelier-nord.test", "last_message_id": float64(42),
		"last_at": "2026-11-05T09:00:00Z", "first_at": "2026-11-01T09:00:00Z",
		"messages": float64(3), "sent": float64(1), "received": float64(2), "unread": float64(1),
		"last_sent": false, "last_is_read": true, "has_attachments": true,
		"snippet": "hello", "due_date": "2026-11-10", "participant_count": float64(1),
		"state_source": "priority",
	} {
		if got := row[key]; got != want {
			t.Errorf("row[%q] = %#v, want %#v", key, got, want)
		}
	}
	for key := range map[string]bool{"conversation_id": true, "last_message_id": true, "due_date": true,
		"participants": true, "organization": true, "state": true, "state_source": true} {
		if _, ok := row[key]; !ok {
			t.Errorf("row is missing %q: %#v", key, row)
		}
	}
	participants, ok := row["participants"].([]any)
	if !ok || len(participants) != 1 {
		t.Fatalf("participants %#v", row["participants"])
	}
	p := participants[0].(map[string]any)
	if p["name"] != "Alice" || p["address"] != "alice@atelier-nord.test" || p["messages"].(float64) != 2 ||
		p["last_at"] != "2026-11-05T09:00:00Z" {
		t.Fatalf("participant %#v", p)
	}
	org, ok := row["organization"].(map[string]any)
	if !ok || org["key"] != "c:atelier nord" || org["label"] != "Atelier Nord" || org["declared"] != true {
		t.Fatalf("organization %#v", row["organization"])
	}
	state, ok := row["state"].(map[string]any)
	if !ok {
		t.Fatalf("state %#v", row["state"])
	}
	if state["state"] != "awaiting_owner" || state["reason"] != "inbound_last" || state["actor"] != "Alice" ||
		state["last_at"] != "2026-11-05T09:00:00Z" || state["last_message_id"].(float64) != 42 ||
		state["reply_to_message_id"].(float64) != 41 || state["visibility"] != "complete" ||
		state["confidence"].(float64) != 0.85 {
		t.Fatalf("state view %#v", state)
	}
	if _, ok := state["actor_name"]; ok {
		t.Fatalf("state exposed actor_name: %#v", state)
	}
	if _, ok := state["messages"]; ok {
		t.Fatalf("state exposed the internal message count: %#v", state)
	}
	counts := body["counts"].(map[string]any)
	for _, st := range threadintel.States {
		if _, ok := counts[string(st)]; !ok {
			t.Errorf("counts missing %q: %#v", st, counts)
		}
	}
	coverage := body["coverage"].(map[string]any)
	if coverage["messages"].(float64) != 3 || coverage["conversations"].(float64) != 1 ||
		coverage["unthreaded"].(float64) != 0 {
		t.Fatalf("coverage %#v", coverage)
	}
}

func TestConversationPageDefaultsAndQuery(t *testing.T) {
	srv, ts, w := newTestServer(t)
	if status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/conversations"}); status != 200 {
		t.Fatalf("status %d: %s", status, data)
	}
	if w.conversations.lastQuery != (conversations.Query{}) {
		t.Fatalf("default query %+v", w.conversations.lastQuery)
	}
	status, _, data := do(t, ts, srv, request{method: "GET",
		path: "/api/conversations?limit=7&before=cursor&state=answered_by_owner&q=Budget"})
	mustStatus(t, status, 200, data)
	got := w.conversations.lastQuery
	if got.Limit != 7 || got.Before != "cursor" || got.State != threadintel.AnsweredByOwner || got.Q != "Budget" {
		t.Fatalf("query %+v", got)
	}
}

func TestConversationPageValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		path   string
		err    error
		status int
		code   string
	}{
		{"zero limit", "/api/conversations?limit=0", nil, 400, "invalid_limit"},
		{"high limit", "/api/conversations?limit=101", nil, 400, "invalid_limit"},
		{"bad limit", "/api/conversations?limit=abc", nil, 400, "invalid_limit"},
		{"state", "/api/conversations?state=nope", conversations.ErrInvalidState, 400, "invalid_state"},
		{"cursor", "/api/conversations?before=nope", conversations.ErrInvalidCursor, 400, "invalid_cursor"},
		{"empty cursor", "/api/conversations?before=", nil, 400, "invalid_cursor"},
		{"query", "/api/conversations?q=bad", conversations.ErrInvalidQuery, 400, "invalid_query"},
		{"internal", "/api/conversations", errors.New("boom"), 500, "internal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, ts, w := newTestServer(t)
			w.conversations.err = tc.err
			status, _, data := do(t, ts, srv, request{method: "GET", path: tc.path})
			mustStatus(t, status, tc.status, data)
			if code := errorCode(t, data); code != tc.code {
				t.Fatalf("code %q, want %q: %s", code, tc.code, data)
			}
			if tc.name == "zero limit" || tc.name == "high limit" || tc.name == "bad limit" || tc.name == "empty cursor" {
				if w.conversations.calls != 0 {
					t.Fatalf("an invalid query reached the service: %d", w.conversations.calls)
				}
			}
			if tc.code == "invalid_limit" && tc.name != "bad limit" {
				var failure struct {
					Error struct {
						Params map[string]string `json:"params"`
					} `json:"error"`
				}
				if err := json.Unmarshal(data, &failure); err != nil {
					t.Fatal(err)
				}
				if failure.Error.Params["name"] != "limit" {
					t.Fatalf("invalid_limit params %#v", failure.Error.Params)
				}
			}
		})
	}
}

func TestConversationPageArraysAndOmittedOrganization(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.conversations.page = conversations.Page{
		Conversations: []conversations.Row{{ConversationID: "C1"}},
	}
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/conversations"})
	mustStatus(t, status, 200, data)
	body := decodeMap(t, data)
	convs := body["conversations"].([]any)
	if len(convs) != 1 {
		t.Fatalf("conversations %#v", convs)
	}
	row := convs[0].(map[string]any)
	if participants, ok := row["participants"].([]any); !ok || len(participants) != 0 {
		t.Fatalf("participants must be an empty array: %#v", row["participants"])
	}
	if _, ok := row["organization"]; ok {
		t.Fatalf("organization must be omitted: %#v", row["organization"])
	}
	counts := body["counts"].(map[string]any)
	if len(counts) != len(threadintel.States) {
		t.Fatalf("counts %#v", counts)
	}
	for _, st := range threadintel.States {
		if got := counts[string(st)]; got != float64(0) {
			t.Errorf("counts[%q] = %#v", st, got)
		}
	}
	coverage := body["coverage"].(map[string]any)
	if coverage["messages"] != float64(0) || coverage["conversations"] != float64(0) || coverage["unthreaded"] != float64(0) {
		t.Fatalf("coverage %#v", coverage)
	}

	w.conversations.page = conversations.Page{}
	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/conversations"})
	mustStatus(t, status, 200, data)
	body = decodeMap(t, data)
	if convs, ok := body["conversations"].([]any); !ok || len(convs) != 0 {
		t.Fatalf("empty conversations must be []: %#v", body["conversations"])
	}
}

func TestConversationPageAuthAndMethod(t *testing.T) {
	srv, ts, _ := newTestServer(t)
	status, header, data := do(t, ts, srv, noAuth(request{method: "GET", path: "/api/conversations"}))
	mustStatus(t, status, 401, data)
	if header.Get("WWW-Authenticate") != "Bearer" || errorCode(t, data) != "unauthorized" {
		t.Fatalf("auth refusal %q %s", header.Get("WWW-Authenticate"), data)
	}
	status, header, data = do(t, ts, srv, request{method: "POST", path: "/api/conversations"})
	mustStatus(t, status, 405, data)
	if header.Get("Allow") != "GET" || errorCode(t, data) != "method_not_allowed" {
		t.Fatalf("method refusal %q %s", header.Get("Allow"), data)
	}
}
