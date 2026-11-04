package api

import (
	"errors"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/threadintel"
)

func threadViewFixture() ThreadView {
	return ThreadView{
		ConversationID: "conv-1",
		Subject:        "Re: Budget review",
		State: ThreadStateView{
			State: threadintel.AwaitingOwner, Reason: threadintel.ReasonInboundLast,
			Actor: "Élodie Martin", LastAt: "2026-11-03T10:00:00Z",
			LastMessageID: 42, ReplyToMessageID: 41,
			Visibility: threadintel.Complete, Confidence: 0.85,
		},
		Messages: []ThreadMessageView{{
			ID: 42, Subject: "Re: Budget review", SenderName: "Élodie Martin",
			SenderAddress: "elodie@example.org", ReceivedAt: "2026-11-03T10:00:00Z",
			Sent: false, IsRead: true, HasAttachments: true, Snippet: "Could you review the budget?",
		}},
		Participants: []ThreadParticipantView{{
			Name: "Élodie Martin", Address: "elodie@example.org", Messages: 3, LastAt: "2026-11-03T10:00:00Z",
		}},
		FirstAt: "2026-11-01T08:00:00Z", LastAt: "2026-11-03T10:00:00Z",
		Count: 5, Truncated: false, NewestMessageID: 42, Newer: 2,
	}
}

func TestThreadRouteShapeQueryAndDefaults(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.threads.view = threadViewFixture()

	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/threads/conv-1?limit=7&before=42"})
	mustStatus(t, status, 200, data)
	if w.threads.calls != 1 || w.threads.lastID != "conv-1" || w.threads.lastLimit != 7 ||
		w.threads.lastBefore != 42 || w.threads.lastAt != 0 {
		t.Fatalf("service call %+v", w.threads)
	}
	body := decodeMap(t, data)
	if body["conversation_id"] != "conv-1" || body["subject"] != "Re: Budget review" ||
		body["first_at"] != "2026-11-01T08:00:00Z" || body["last_at"] != "2026-11-03T10:00:00Z" ||
		body["count"].(float64) != 5 || body["truncated"] != false ||
		body["newest_message_id"].(float64) != 42 || body["newer"].(float64) != 2 {
		t.Fatalf("thread header %#v", body)
	}
	state, _ := body["state"].(map[string]any)
	if state["state"] != "awaiting_owner" || state["reason"] != "inbound_last" ||
		state["actor"] != "Élodie Martin" || state["last_at"] != "2026-11-03T10:00:00Z" ||
		state["last_message_id"].(float64) != 42 || state["reply_to_message_id"].(float64) != 41 ||
		state["visibility"] != "complete" || state["confidence"].(float64) != 0.85 {
		t.Fatalf("state %#v", state)
	}
	messages, _ := body["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("messages %#v", messages)
	}
	message, _ := messages[0].(map[string]any)
	if message["id"].(float64) != 42 || message["subject"] != "Re: Budget review" ||
		message["sender_name"] != "Élodie Martin" || message["sender_address"] != "elodie@example.org" ||
		message["received_at"] != "2026-11-03T10:00:00Z" || message["sent"] != false ||
		message["is_read"] != true || message["has_attachments"] != true ||
		message["snippet"] != "Could you review the budget?" {
		t.Fatalf("message %#v", message)
	}
	participants, _ := body["participants"].([]any)
	if len(participants) != 1 {
		t.Fatalf("participants %#v", participants)
	}
	participant, _ := participants[0].(map[string]any)
	if participant["name"] != "Élodie Martin" || participant["address"] != "elodie@example.org" ||
		participant["messages"].(float64) != 3 || participant["last_at"] != "2026-11-03T10:00:00Z" {
		t.Fatalf("participant %#v", participant)
	}

	// An anchor page ends at the opened message, inclusively; the service,
	// not the handler, resolves it.
	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/threads/conv-1?at=42"})
	mustStatus(t, status, 200, data)
	if w.threads.lastBefore != 0 || w.threads.lastAt != 42 {
		t.Fatalf("anchor call %+v", w.threads)
	}

	// Defaults and empty arrays: a conversation with no page payload still
	// encodes messages and participants as arrays, never null.
	w.threads.view = ThreadView{ConversationID: "conv-1"}
	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/threads/conv-1"})
	mustStatus(t, status, 200, data)
	if w.threads.lastLimit != 50 || w.threads.lastBefore != 0 || w.threads.lastAt != 0 {
		t.Fatalf("defaults %+v", w.threads)
	}
	body = decodeMap(t, data)
	if messages, ok := body["messages"].([]any); !ok || len(messages) != 0 {
		t.Fatalf("messages %#v", body["messages"])
	}
	if participants, ok := body["participants"].([]any); !ok || len(participants) != 0 {
		t.Fatalf("participants %#v", body["participants"])
	}
}

func TestThreadRouteValidation(t *testing.T) {
	srv, ts, w := newTestServer(t)
	cases := []struct {
		name string
		path string
		code string
	}{
		{"zero limit", "/api/threads/C1?limit=0", "invalid_limit"},
		{"over max limit", "/api/threads/C1?limit=101", "invalid_limit"},
		{"bad limit", "/api/threads/C1?limit=x", "invalid_limit"},
		{"negative before", "/api/threads/C1?before=-1", "invalid_before"},
		{"bad before", "/api/threads/C1?before=x", "invalid_before"},
		{"empty before", "/api/threads/C1?before=", "invalid_before"},
		{"negative at", "/api/threads/C1?at=-1", "invalid_at"},
		{"bad at", "/api/threads/C1?at=x", "invalid_at"},
		{"empty at", "/api/threads/C1?at=", "invalid_at"},
		{"before and at", "/api/threads/C1?before=7&at=9", "invalid_cursor"},
		{"long thread", "/api/threads/" + strings.Repeat("a", store.MaxConversationIDChars+1), "invalid_thread"},
		{"empty thread", "/api/threads/", "invalid_thread"},
		{"control thread", "/api/threads/bad%00id", "invalid_thread"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, _, data := do(t, ts, srv, request{method: "GET", path: tc.path})
			mustStatus(t, status, 400, data)
			if code := errorCode(t, data); code != tc.code {
				t.Fatalf("code %q, want %q: %s", code, tc.code, data)
			}
		})
	}
	if w.threads.calls != 0 {
		t.Fatalf("an invalid request reached the thread service: %d", w.threads.calls)
	}
}

func TestThreadRouteAuthGateAndErrors(t *testing.T) {
	srv, ts, w := newTestServer(t)

	status, _, data := do(t, ts, srv, noAuth(request{method: "GET", path: "/api/threads/C1"}))
	mustStatus(t, status, 401, data)
	if code := errorCode(t, data); code != "unauthorized" {
		t.Fatalf("code %q", code)
	}
	if w.threads.calls != 0 {
		t.Fatalf("an unauthenticated request reached the thread service: %d", w.threads.calls)
	}

	w.learning.allowErr = lockedGate()
	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/threads/C1"})
	mustStatus(t, status, 423, data)
	if code := errorCode(t, data); code != "learning_locked" {
		t.Fatalf("code %q", code)
	}
	if w.threads.calls != 0 {
		t.Fatalf("a locked gate reached the thread service: %d", w.threads.calls)
	}
	w.learning.allowErr = nil

	w.threads.err = store.ErrThreadNotFound
	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/threads/unknown"})
	mustStatus(t, status, 404, data)
	if code := errorCode(t, data); code != "unknown_thread" {
		t.Fatalf("code %q", code)
	}

	w.threads.err = store.ErrInvalidConversationID
	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/threads/C1"})
	mustStatus(t, status, 400, data)
	if code := errorCode(t, data); code != "invalid_thread" {
		t.Fatalf("code %q", code)
	}

	w.threads.err = store.ErrInvalidThreadCursor
	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/threads/C1?before=999"})
	mustStatus(t, status, 400, data)
	if code := errorCode(t, data); code != "invalid_before" {
		t.Fatalf("code %q", code)
	}

	w.threads.err = errors.New("boom")
	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/threads/C1"})
	mustStatus(t, status, 500, data)
	if code := errorCode(t, data); code != "internal" {
		t.Fatalf("code %q", code)
	}

	w.threads.err = nil
	before := w.threads.calls
	status, header, data := do(t, ts, srv, request{method: "POST", path: "/api/threads/C1"})
	mustStatus(t, status, 405, data)
	if header.Get("Allow") != "GET" {
		t.Fatalf("Allow %q", header.Get("Allow"))
	}
	if code := errorCode(t, data); code != "method_not_allowed" {
		t.Fatalf("code %q", code)
	}
	if w.threads.calls != before {
		t.Fatalf("a wrong method reached the thread service: %d -> %d", before, w.threads.calls)
	}
}
