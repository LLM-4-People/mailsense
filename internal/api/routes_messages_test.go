package api

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

func TestMessageListEndpointShapeAndRequest(t *testing.T) {
	srv, ts, w := newTestServer(t)
	key := source.StoreKey("src", "store-1")
	cursor := store.Cursor{Instant: "2026-03-01T09:00:00Z", ID: 5, Role: mail.RoleInbox, Store: key,
		Sender: "demo@example.test", Unread: true}.String()
	w.messages.list = MessageListView{
		Messages: []MessageListRow{{
			RowID: 5, Subject: "synthetic", SenderName: "Élodie", SenderAddress: "elodie@example.org",
			Instant:    "2026-03-01T09:00:00Z",
			ReceivedAt: "2026-03-01T09:00:00Z", Role: mail.RoleInbox, Snippet: "a snippet",
		}},
		Total: 3, Counts: map[string]int{"inbox": 2, "sent": 1},
		NextBefore: "next-cursor", HasMore: true,
	}
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/messages?role=inbox&store=" + key +
		"&sender=Demo%40Example.Test&unread=true&limit=2&counts=1&before=" + url.QueryEscape(cursor)})
	mustStatus(t, status, 200, data)

	if w.messages.listCalls != 1 {
		t.Fatalf("service calls %d", w.messages.listCalls)
	}
	q := w.messages.lastList
	if q.Role != mail.RoleInbox || q.Store != key || q.Sender != "demo@example.test" || !q.Unread || q.Limit != 2 || !q.Counts ||
		q.Before.ID != 5 || q.Before.Instant != "2026-03-01T09:00:00Z" || q.Before.Role != mail.RoleInbox || q.Before.Store != key ||
		q.Before.Sender != "demo@example.test" || !q.Before.Unread {
		t.Fatalf("service query %+v", q)
	}

	body := decodeMap(t, data)
	for _, key := range []string{"messages", "total", "counts", "next_before", "has_more"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("response misses %q: %s", key, data)
		}
	}
	rows, _ := body["messages"].([]any)
	if len(rows) != 1 {
		t.Fatalf("messages %#v", body["messages"])
	}
	row, _ := rows[0].(map[string]any)
	for _, key := range []string{"body", "body_html", "headers", "attachments",
		// The list row is a screen contract: the store/folder labels and the
		// representing address are not rendered by any list.
		"sent_representing_address", "folder_path", "store_key", "store_name"} {
		if _, ok := row[key]; ok {
			t.Fatalf("list row exposes %q: %s", key, data)
		}
	}
	if row["role"] != "inbox" || row["sender_address"] != "elodie@example.org" || body["total"] != float64(3) ||
		body["has_more"] != true || body["next_before"] != "next-cursor" {
		t.Fatalf("row/response %#v", row)
	}
}

func TestMessageListEndpointDefaultsAndEmptyArrays(t *testing.T) {
	srv, ts, w := newTestServer(t)
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/messages"})
	mustStatus(t, status, 200, data)
	if q := w.messages.lastList; q.Role != "" || q.Store != "" || q.Sender != "" || q.Unread || q.Limit != 0 || q.Counts || !q.Before.IsZero() {
		t.Fatalf("default query %+v", q)
	}
	body := decodeMap(t, data)
	if rows, ok := body["messages"].([]any); !ok || len(rows) != 0 {
		t.Fatalf("messages must be an empty array: %s", data)
	}
	if _, ok := body["counts"]; ok {
		t.Fatalf("counts must be absent unless requested: %s", data)
	}
	if body["total"] != float64(0) || body["has_more"] != false || body["next_before"] != "" {
		t.Fatalf("empty page %s", data)
	}

	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/messages?role=all&counts=1"})
	mustStatus(t, status, 200, data)
	if got := w.messages.lastList.Role; got != "all" || !w.messages.lastList.Counts {
		t.Fatalf("role all/counts request %+v", w.messages.lastList)
	}
}

func TestMessageListValidation(t *testing.T) {
	srv, ts, w := newTestServer(t)
	validCursor := store.Cursor{Instant: "2026-03-01T09:00:00Z", ID: 5}.String()
	cases := []struct {
		name string
		path string
		code string
	}{
		{"bad role", "/api/messages?role=nope", "invalid_role"},
		{"zero limit", "/api/messages?limit=0", "invalid_limit"},
		{"large limit", "/api/messages?limit=201", "invalid_limit"},
		{"non-numeric limit", "/api/messages?limit=x", "invalid_limit"},
		{"empty cursor", "/api/messages?before=", "invalid_cursor"},
		{"corrupt cursor", "/api/messages?before=not-a-cursor", "invalid_cursor"},
		{"bad store", "/api/messages?store=store-1", "invalid_store"},
		{"empty store", "/api/messages?store=", "invalid_store"},
		{"empty sender", "/api/messages?sender=", "invalid_sender"},
		{"blank sender", "/api/messages?sender=%20%20", "invalid_sender"},
		{"lone at sender", "/api/messages?sender=%40", "invalid_sender"},
		{"long sender", "/api/messages?sender=" + url.QueryEscape(strings.Repeat("a", mail.MaxSenderQueryRunes+1)), "invalid_sender"},
		{"control sender", "/api/messages?sender=" + url.QueryEscape("a\x00b@example.org"), "invalid_sender"},
		{"bad counts", "/api/messages?counts=maybe", "invalid_counts"},
		{"empty unread", "/api/messages?unread=", "invalid_unread"},
		{"blank unread", "/api/messages?unread=%20", "invalid_unread"},
		{"bad unread", "/api/messages?unread=read", "invalid_unread"},
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

	// A syntactically valid cursor the store rejects belongs to another
	// role/store; the handler maps the store's typed error.
	w.messages.err = store.ErrInvalidCursor
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/messages?before=" + url.QueryEscape(validCursor)})
	mustStatus(t, status, 400, data)
	if code := errorCode(t, data); code != "invalid_cursor" {
		t.Fatalf("code %q", code)
	}

	// The store re-validates the sender the service may have received from
	// another caller; its typed error has the same wire code.
	w.messages.err = store.ErrInvalidSender
	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/messages?sender=demo@example.test"})
	mustStatus(t, status, 400, data)
	if code := errorCode(t, data); code != "invalid_sender" {
		t.Fatalf("code %q", code)
	}
	w.messages.err = nil

	status, _, data = do(t, ts, srv, noAuth(request{method: "GET", path: "/api/messages"}))
	mustStatus(t, status, 401, data)
	if code := errorCode(t, data); code != "unauthorized" {
		t.Fatalf("code %q", code)
	}
}

func TestMessageListSenderParameterAndValidation(t *testing.T) {
	srv, ts, w := newTestServer(t)
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/messages?sender=Demo%40Example.Test&counts=1"})
	mustStatus(t, status, 200, data)
	if got := w.messages.lastList.Sender; got != "demo@example.test" {
		t.Fatalf("sender %q, want the normalized address", got)
	}
	if w.messages.listCalls != 1 {
		t.Fatalf("service calls %d", w.messages.listCalls)
	}
	// A domain and a non-ASCII address keep their normalized form too.
	for _, tc := range []struct{ raw, want string }{
		{"%40Example.Test", "@example.test"},
		{"%C3%89lodie%40Exemple.TEST", "élodie@exemple.test"},
		{"elodie%2Btag%40example.org", "elodie+tag@example.org"},
	} {
		status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/messages?sender=" + tc.raw})
		mustStatus(t, status, 200, data)
		if got := w.messages.lastList.Sender; got != tc.want {
			t.Fatalf("sender %q normalized to %q, want %q", tc.raw, got, tc.want)
		}
	}

	calls := w.messages.listCalls
	for _, tc := range []struct{ name, raw string }{
		{"empty", ""},
		{"blank", "%20%20"},
		{"lone at", "%40"},
		{"long", url.QueryEscape(strings.Repeat("a", mail.MaxSenderQueryRunes+1))},
		{"control", url.QueryEscape("a\x00b@example.org")},
		{"leading control", url.QueryEscape("\x00a@example.org")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/messages?sender=" + tc.raw})
			mustStatus(t, status, 400, data)
			if code := errorCode(t, data); code != "invalid_sender" {
				t.Fatalf("code %q, want invalid_sender: %s", code, data)
			}
			var body struct {
				Error struct {
					Params map[string]string `json:"params"`
				} `json:"error"`
			}
			if err := json.Unmarshal(data, &body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Params["name"] != "sender" || body.Error.Params["max"] != strconv.Itoa(mail.MaxSenderQueryRunes) {
				t.Fatalf("error params %#v: %s", body.Error.Params, data)
			}
		})
	}
	if w.messages.listCalls != calls {
		t.Fatalf("an invalid sender reached the service (%d extra calls)", w.messages.listCalls-calls)
	}
}

func TestMessageListUnreadParameterAndValidation(t *testing.T) {
	srv, ts, w := newTestServer(t)
	for _, raw := range []string{"true", "TRUE", "1"} {
		status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/messages?unread=" + raw})
		mustStatus(t, status, 200, data)
		if !w.messages.lastList.Unread {
			t.Fatalf("unread=%q did not reach the service as true", raw)
		}
	}
	for _, raw := range []string{"false", "FALSE", "0"} {
		status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/messages?unread=" + raw})
		mustStatus(t, status, 200, data)
		if w.messages.lastList.Unread {
			t.Fatalf("unread=%q did not reach the service as false", raw)
		}
	}
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/messages"})
	mustStatus(t, status, 200, data)
	if w.messages.lastList.Unread {
		t.Fatal("an absent unread filter reached the service as true")
	}

	calls := w.messages.listCalls
	for _, tc := range []struct{ name, raw string }{
		{"empty", ""},
		{"blank", "%20"},
		{"tab", "%09"},
		{"bad", "read"},
		{"read-only wording", "read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/messages?unread=" + tc.raw})
			mustStatus(t, status, 400, data)
			if code := errorCode(t, data); code != "invalid_unread" {
				t.Fatalf("code %q, want invalid_unread: %s", code, data)
			}
			var body struct {
				Error struct {
					Params map[string]string `json:"params"`
				} `json:"error"`
			}
			if err := json.Unmarshal(data, &body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Params["name"] != "unread" {
				t.Fatalf("error params %#v: %s", body.Error.Params, data)
			}
		})
	}
	if w.messages.listCalls != calls {
		t.Fatalf("an invalid unread reached the service (%d extra calls)", w.messages.listCalls-calls)
	}
}

func TestMessageListRowsCarryNoRecipients(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.messages.list = MessageListView{Messages: []MessageListRow{{RowID: 1}}}
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/messages"})
	mustStatus(t, status, 200, data)
	if strings.Contains(string(data), `"recipients"`) {
		t.Fatalf("the list wire still carries recipients: %s", data)
	}
	var view struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(data, &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Messages) != 1 {
		t.Fatalf("messages %s", data)
	}
}

// TestBooleanFilterErrorsCarryTheirName: the message list has two boolean
// filters, and a bad value must be the same shape for both: invalid_<name>
// with params.name, so the client composes one message from the code. counts
// used to omit the params its twin carried.
func TestBooleanFilterErrorsCarryTheirName(t *testing.T) {
	srv, ts, w := newTestServer(t)
	for _, tc := range []struct{ name, path string }{
		{"counts", "/api/messages?counts=maybe"},
		{"unread", "/api/messages?unread=read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, _, data := do(t, ts, srv, request{method: "GET", path: tc.path})
			mustStatus(t, status, 400, data)
			got := decodeAPIError(t, data)
			if got.Code != "invalid_"+tc.name {
				t.Fatalf("code %q, want invalid_%s: %s", got.Code, tc.name, data)
			}
			if len(got.Params) != 1 || got.Params["name"] != tc.name {
				t.Fatalf("params %#v, want name=%s: %s", got.Params, tc.name, data)
			}
		})
	}
	if w.messages.listCalls != 0 {
		t.Fatalf("a refused boolean filter reached the service %d times", w.messages.listCalls)
	}
}
