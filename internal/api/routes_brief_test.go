package api

import (
	"errors"
	"fmt"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/brief"
)

func briefFixture() brief.Result {
	return brief.Result{
		GeneratedAt: "2026-10-27T12:00:00Z",
		Must: brief.Section{Count: 2, Items: []brief.Item{{
			ID: "due-1", Kind: "commitment", Title: "Invoice", Subject: "Invoice",
			Organization: &brief.Org{Key: "c:atelier nord", Label: "Atelier Nord"},
			Person:       "alice@atelier-nord.test", Due: "2026-11-01T09:00:00Z", Status: "overdue",
			Direction: "owner", ConversationID: "C1", MessageID: 11, Evidence: []int64{11},
		}}},
		Waiting: brief.Section{Count: 1, Items: []brief.Item{{
			ID: "reply-1", Kind: "commitment", Title: "Budget", Subject: "Budget",
			Person: "bob@example.test", Status: "open", Direction: "other",
			ConversationID: "C2", MessageID: 12, Evidence: []int64{12}, LastAt: "2026-10-27T07:00:00Z",
		}}},
		Changed: brief.Section{Count: 1, Since: "2026-10-20T12:00:00Z", Items: []brief.Item{{
			ID: "activity:C3", Kind: "activity", Title: "Latest", Subject: "Latest",
			ConversationID: "C3", MessageID: 13, Evidence: []int64{13},
			LastAt: "2026-10-27T10:00:00Z", State: "awaiting_owner", StateReason: "inbound_last",
		}}},
		Relationships: brief.Section{Count: 1, Items: []brief.Item{{
			ID: "organization:c:atelier nord", Kind: "organization", Title: "Atelier Nord",
			Organization: &brief.Org{Key: "c:atelier nord", Label: "Atelier Nord"},
			Count:        3, Overdue: 1, StoredDue: 1,
		}}},
		Topics: brief.Section{Count: 7, Total: 120, Suggested: true, Scope: "owner_mailbox", Items: []brief.Item{{
			ID: "topic:project_work", Kind: "topic", Title: "project_work", Count: 4, Suggested: true,
			Evidence: []int64{1, 2, 3},
		}}},
	}
}

func TestBriefShape(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.brief.res = briefFixture()

	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/brief?since=2026-10-20T12:00:00Z"})
	mustStatus(t, status, 200, data)
	if w.brief.calls != 1 || w.brief.lastSince != "2026-10-20T12:00:00Z" {
		t.Fatalf("brief call %d since %q", w.brief.calls, w.brief.lastSince)
	}
	body := decodeMap(t, data)
	if body["generated_at"] != "2026-10-27T12:00:00Z" {
		t.Fatalf("generated_at %#v", body["generated_at"])
	}
	for name, count := range map[string]float64{"must": 2, "waiting": 1, "changed": 1, "relationships": 1, "topics": 7} {
		section, ok := body[name].(map[string]any)
		if !ok {
			t.Fatalf("%s is not an object: %#v", name, body[name])
		}
		if section["count"] != count {
			t.Errorf("%s.count = %#v, want %v", name, section["count"], count)
		}
		if _, ok := section["items"].([]any); !ok {
			t.Errorf("%s.items is not an array: %#v", name, section["items"])
		}
	}
	must := body["must"].(map[string]any)["items"].([]any)[0].(map[string]any)
	for key, want := range map[string]any{
		"id": "due-1", "kind": "commitment", "title": "Invoice", "status": "overdue",
		"direction": "owner", "conversation_id": "C1", "message_id": float64(11),
		"person": "alice@atelier-nord.test", "due": "2026-11-01T09:00:00Z",
	} {
		if got := must[key]; got != want {
			t.Errorf("must[%q] = %#v, want %#v", key, got, want)
		}
	}
	org, ok := must["organization"].(map[string]any)
	if !ok || org["key"] != "c:atelier nord" || org["label"] != "Atelier Nord" {
		t.Fatalf("must organization %#v", must["organization"])
	}
	changedSection := body["changed"].(map[string]any)
	if changedSection["since"] != "2026-10-20T12:00:00Z" {
		t.Fatalf("changed.since %#v", changedSection["since"])
	}
	changed := changedSection["items"].([]any)[0].(map[string]any)
	if changed["kind"] != "activity" || changed["state"] != "awaiting_owner" ||
		changed["state_reason"] != "inbound_last" || changed["message_id"] != float64(13) {
		t.Fatalf("changed item %#v", changed)
	}
	relationship := body["relationships"].(map[string]any)["items"].([]any)[0].(map[string]any)
	for key, want := range map[string]any{
		"kind": "organization", "count": float64(3), "overdue": float64(1), "stored_due": float64(1),
	} {
		if got := relationship[key]; got != want {
			t.Errorf("relationship[%q] = %#v, want %#v", key, got, want)
		}
	}
	for _, dead := range []string{"conversations", "messages", "awaiting_owner", "awaiting_other"} {
		if _, ok := relationship[dead]; ok {
			t.Errorf("relationship[%q] exposes a dead digest fact", dead)
		}
	}
	topics := body["topics"].(map[string]any)
	if topics["suggested"] != true || topics["scope"] != "owner_mailbox" || topics["total"] != float64(120) {
		t.Fatalf("topics section %#v", topics)
	}
	topic := topics["items"].([]any)[0].(map[string]any)
	if topic["suggested"] != true || topic["id"] != "topic:project_work" || topic["count"] != float64(4) {
		t.Fatalf("topic item %#v", topic)
	}

	// since is optional; the service owns the default window.
	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/brief"})
	mustStatus(t, status, 200, data)
	if w.brief.lastSince != "" || w.brief.calls != 2 {
		t.Fatalf("default since reached the service as %q (calls %d)", w.brief.lastSince, w.brief.calls)
	}
}

func TestBriefInvalidSince(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.brief.err = fmt.Errorf("wrapped: %w", brief.ErrInvalidSince)
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/brief?since=yesterday"})
	mustStatus(t, status, 400, data)
	if code := errorCode(t, data); code != "invalid_since_time" {
		t.Fatalf("code %q, want invalid_since_time: %s", code, data)
	}

	w.brief.err = errors.New("store unavailable")
	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/brief"})
	mustStatus(t, status, 500, data)
	if code := errorCode(t, data); code != "internal" {
		t.Fatalf("code %q, want internal: %s", code, data)
	}
}

func TestBriefEmptyArraysAndSections(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.brief.res = brief.Result{Must: brief.Section{Items: []brief.Item{{ID: "no-evidence"}}}}

	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/brief"})
	mustStatus(t, status, 200, data)
	body := decodeMap(t, data)
	for _, name := range []string{"must", "waiting", "changed", "relationships", "topics"} {
		section, ok := body[name].(map[string]any)
		if !ok {
			t.Fatalf("%s missing: %#v", name, body)
		}
		if _, ok := section["items"].([]any); !ok {
			t.Fatalf("%s.items must be an array: %#v", name, section["items"])
		}
	}
	item := body["must"].(map[string]any)["items"].([]any)[0].(map[string]any)
	if evidence, ok := item["evidence"].([]any); !ok || len(evidence) != 0 {
		t.Fatalf("evidence must be an array, never null: %#v", item["evidence"])
	}
}
