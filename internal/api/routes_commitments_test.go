package api

import (
	"errors"
	"fmt"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/commitments"
)

func commitmentsFixture() commitments.Result {
	return commitments.Result{
		Items: []commitments.Commitment{
			{
				ID: "due-1", Kind: commitments.KindDue, Direction: commitments.DirectionOwner,
				Status: commitments.StatusOverdue, Title: "Invoice", Subject: "Invoice",
				Due: "2026-11-01T09:00:00Z", ConversationID: "C1", MessageID: 11,
				OrganizationKey: "c:atelier nord", OrganizationLabel: "Atelier Nord",
				Person: "alice@atelier-nord.test", Evidence: []int64{11},
			},
			{
				ID: "reply-1", Kind: commitments.KindReply, Direction: commitments.DirectionOther,
				Status: commitments.StatusOpen, Title: "Budget", Subject: "Budget",
				ConversationID: "C2", MessageID: 12, Person: "bob@example.test",
				LastAt: "2026-10-27T07:00:00Z", Evidence: []int64{12},
			},
			{
				ID: "decision-1", Kind: commitments.KindDecision, Direction: commitments.DirectionOwner,
				Status: commitments.StatusOpen, Title: "Approve", Subject: "Approve",
				MessageID: 13, Person: "carol@example.test", LastAt: "2026-10-27T06:00:00Z",
				Evidence: []int64{13},
			},
		},
		Counts: map[string]int{
			"due": 1, "reply": 1, "decision": 1,
			"owner": 2, "other": 1,
			"open": 2, "overdue": 1, "done": 0, "unknown": 0,
		},
		GeneratedAt: "2026-10-27T08:00:00Z",
	}
}

func TestCommitmentsShape(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.commitments.res = commitmentsFixture()

	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/commitments"})
	mustStatus(t, status, 200, data)
	body := decodeMap(t, data)
	if body["generated_at"] != "2026-10-27T08:00:00Z" {
		t.Fatalf("generated_at %#v", body["generated_at"])
	}
	items, ok := body["items"].([]any)
	if !ok || len(items) != 3 {
		t.Fatalf("items %#v", body["items"])
	}

	due := items[0].(map[string]any)
	for key, want := range map[string]any{
		"id": "due-1", "kind": "due", "direction": "owner", "status": "overdue",
		"title": "Invoice", "subject": "Invoice", "due": "2026-11-01T09:00:00Z",
		"conversation_id": "C1", "message_id": float64(11),
		"organization_key": "c:atelier nord", "organization_label": "Atelier Nord",
		"person": "alice@atelier-nord.test",
	} {
		if got := due[key]; got != want {
			t.Errorf("due[%q] = %#v, want %#v", key, got, want)
		}
	}
	evidence, ok := due["evidence"].([]any)
	if !ok || len(evidence) != 1 || evidence[0] != float64(11) {
		t.Fatalf("due evidence %#v", due["evidence"])
	}

	reply := items[1].(map[string]any)
	if reply["kind"] != "reply" || reply["direction"] != "other" || reply["status"] != "open" ||
		reply["conversation_id"] != "C2" || reply["last_at"] != "2026-10-27T07:00:00Z" || reply["person"] != "bob@example.test" {
		t.Fatalf("reply %#v", reply)
	}
	if _, ok := reply["due"]; ok {
		t.Fatalf("a reply carries no due date: %#v", reply["due"])
	}

	decision := items[2].(map[string]any)
	if decision["kind"] != "decision" || decision["direction"] != "owner" || decision["status"] != "open" ||
		decision["message_id"] != float64(13) || decision["last_at"] != "2026-10-27T06:00:00Z" {
		t.Fatalf("decision %#v", decision)
	}
	if _, ok := decision["conversation_id"]; ok {
		t.Fatalf("a decision is not conversation-linked: %#v", decision["conversation_id"])
	}

	counts, ok := body["counts"].(map[string]any)
	if !ok {
		t.Fatalf("counts %#v", body["counts"])
	}
	for key, want := range map[string]float64{
		"due": 1, "reply": 1, "decision": 1,
		"owner": 2, "other": 1,
		"open": 2, "overdue": 1, "done": 0, "unknown": 0,
	} {
		if got := counts[key]; got != want {
			t.Errorf("counts[%q] = %#v, want %v", key, got, want)
		}
	}
}

func TestCommitmentsQueryDefaultsAndFilters(t *testing.T) {
	srv, ts, w := newTestServer(t)

	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/commitments"})
	mustStatus(t, status, 200, data)
	if want := (commitments.Filter{Limit: commitments.DefaultLimit}); w.commitments.lastFilter != want {
		t.Fatalf("default filter %+v, want %+v", w.commitments.lastFilter, want)
	}

	for _, tc := range []struct {
		path string
		want commitments.Filter
	}{
		{"/api/commitments?kind=due", commitments.Filter{Kind: commitments.KindDue, Limit: commitments.DefaultLimit}},
		{"/api/commitments?direction=owner", commitments.Filter{Direction: commitments.DirectionOwner, Limit: commitments.DefaultLimit}},
		{"/api/commitments?status=overdue", commitments.Filter{Status: commitments.StatusOverdue, Limit: commitments.DefaultLimit}},
	} {
		status, _, data = do(t, ts, srv, request{method: "GET", path: tc.path})
		mustStatus(t, status, 200, data)
		if w.commitments.lastFilter != tc.want {
			t.Fatalf("%s: filter %+v, want %+v", tc.path, w.commitments.lastFilter, tc.want)
		}
	}

	status, _, data = do(t, ts, srv, request{method: "GET",
		path: "/api/commitments?kind=reply&direction=other&status=open&limit=7"})
	mustStatus(t, status, 200, data)
	want := commitments.Filter{Kind: commitments.KindReply, Direction: commitments.DirectionOther,
		Status: commitments.StatusOpen, Limit: 7}
	if w.commitments.lastFilter != want {
		t.Fatalf("filter %+v, want %+v", w.commitments.lastFilter, want)
	}

	// Existing query conventions: the first duplicate wins and unknown
	// parameters are ignored. An empty value leaves that facet unfiltered.
	status, _, data = do(t, ts, srv, request{method: "GET",
		path: "/api/commitments?kind=due&kind=reply&nope=1"})
	mustStatus(t, status, 200, data)
	if w.commitments.lastFilter.Kind != commitments.KindDue || w.commitments.lastFilter.Limit != commitments.DefaultLimit {
		t.Fatalf("duplicate/unknown query %+v", w.commitments.lastFilter)
	}

	status, _, data = do(t, ts, srv, request{method: "GET",
		path: "/api/commitments?kind=&direction=&status="})
	mustStatus(t, status, 200, data)
	if want := (commitments.Filter{Limit: commitments.DefaultLimit}); w.commitments.lastFilter != want {
		t.Fatalf("empty filters %+v, want %+v", w.commitments.lastFilter, want)
	}
}

func TestCommitmentsValidation(t *testing.T) {
	srv, ts, w := newTestServer(t)
	cases := []struct {
		name   string
		path   string
		status int
		code   string
	}{
		{"kind", "/api/commitments?kind=promise", 400, "invalid_kind"},
		{"direction", "/api/commitments?direction=them", 400, "invalid_direction"},
		{"status", "/api/commitments?status=closed", 400, "invalid_status"},
		{"zero limit", "/api/commitments?limit=0", 400, "invalid_limit"},
		{"high limit", "/api/commitments?limit=501", 400, "invalid_limit"},
		{"bad limit", "/api/commitments?limit=nope", 400, "invalid_limit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, _, data := do(t, ts, srv, request{method: "GET", path: tc.path})
			mustStatus(t, status, tc.status, data)
			if code := errorCode(t, data); code != tc.code {
				t.Fatalf("code %q, want %q: %s", code, tc.code, data)
			}
		})
	}
	if w.commitments.calls != 0 {
		t.Fatalf("an invalid query reached the service: %d", w.commitments.calls)
	}

	// The handler validates first; a different service implementation may
	// still return the typed sentinels, which must map the same way.
	codes := []struct {
		err  error
		code string
	}{
		{fmt.Errorf("wrapped: %w", commitments.ErrInvalidKind), "invalid_kind"},
		{fmt.Errorf("wrapped: %w", commitments.ErrInvalidDirection), "invalid_direction"},
		{fmt.Errorf("wrapped: %w", commitments.ErrInvalidStatus), "invalid_status"},
		{fmt.Errorf("wrapped: %w", commitments.ErrInvalidLimit), "invalid_limit"},
		{errors.New("boom"), "internal"},
	}
	for _, tc := range codes {
		w.commitments.err = tc.err
		status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/commitments"})
		want := 400
		if tc.code == "internal" {
			want = 500
		}
		mustStatus(t, status, want, data)
		if code := errorCode(t, data); code != tc.code {
			t.Fatalf("%v: code %q, want %q: %s", tc.err, code, tc.code, data)
		}
	}
}

func TestCommitmentsEmptyArrays(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.commitments.res = commitments.Result{}

	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/commitments"})
	mustStatus(t, status, 200, data)
	body := decodeMap(t, data)
	if items, ok := body["items"].([]any); !ok || len(items) != 0 {
		t.Fatalf("empty items must be []: %#v", body["items"])
	}
	if counts, ok := body["counts"].(map[string]any); !ok || len(counts) != 0 {
		t.Fatalf("empty counts must be {}: %#v", body["counts"])
	}
	if got, ok := body["generated_at"]; !ok || got != "" {
		t.Fatalf("generated_at %#v", got)
	}
}

func TestCommitmentsAuthGateAndMethod(t *testing.T) {
	srv, ts, w := newTestServer(t)
	status, header, data := do(t, ts, srv, noAuth(request{method: "GET", path: "/api/commitments"}))
	mustStatus(t, status, 401, data)
	if header.Get("WWW-Authenticate") != "Bearer" || errorCode(t, data) != "unauthorized" {
		t.Fatalf("auth refusal %q %s", header.Get("WWW-Authenticate"), data)
	}
	if w.commitments.calls != 0 {
		t.Fatal("an unauthenticated request reached the service")
	}

	w.learning.allowErr = lockedGate()
	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/commitments"})
	mustStatus(t, status, 423, data)
	if errorCode(t, data) != "learning_locked" || w.commitments.calls != 0 {
		t.Fatalf("gate refusal %s (calls %d)", data, w.commitments.calls)
	}

	w.learning.allowErr = nil
	status, header, data = do(t, ts, srv, request{method: "POST", path: "/api/commitments"})
	mustStatus(t, status, 405, data)
	if header.Get("Allow") != "GET" || errorCode(t, data) != "method_not_allowed" {
		t.Fatalf("method refusal %q %s", header.Get("Allow"), data)
	}
}
