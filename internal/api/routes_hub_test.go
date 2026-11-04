package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/hub"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/store"
)

func hubWorld(t *testing.T) (*Server, *world, func(request) (int, []byte)) {
	t.Helper()
	srv, ts, w := newTestServer(t)
	w.hub.hub = hub.Hub{
		Gate: hub.Gate{Open: false, State: string(learning.Stuck),
			Labels:    hub.LabelTotals{Manual: 2, Judge: 3, Needed: 10, Review: 3},
			Questions: []hub.Question{{Question: "category", Manual: 2, Judge: 3, Needed: 10, Review: 3}},
			Blockers: []hub.BlockerView{{Code: "layer1_too_few_eval_labels", Subject: "category",
				Measure: &learning.Measure{Have: 2, Need: 10, Unit: "evaluation labels"}}}},
		Mailbox: hub.Mailbox{Messages: 5, Stores: 1, Synced: true},
		Recent: []hub.Answer{
			{ID: 12, MessageID: 1, Question: "category", Source: store.LabelJudge, Value: json.RawMessage(`"work"`), At: "2026-10-26T09:12:00Z"},
			{ID: 11, MessageID: 1, Question: "retired_question", Source: store.LabelManual, Value: json.RawMessage(`true`), At: "2026-10-26T09:10:00Z"},
		},
	}
	w.hub.answers = hub.AnswerPage{Items: []hub.Answer{
		{ID: 12, MessageID: 1, Question: "category", Source: store.LabelJudge, Value: json.RawMessage(`"work"`), At: "2026-10-26T09:12:00Z"},
		{ID: 11, MessageID: 1, Question: "retired_question", Source: store.LabelManual, Value: json.RawMessage(`true`), At: "2026-10-26T09:10:00Z"},
	}, NextCursor: 11, HasMore: true}
	w.hub.summary = hub.LabelSummary{
		LabelTotals: hub.LabelTotals{Manual: 2, Judge: 3, Needed: 10, Review: 3},
		Questions:   []hub.Question{{Question: "category", Manual: 2, Judge: 3, Needed: 10, Review: 3}},
	}
	call := func(in request) (int, []byte) {
		status, _, data := do(t, ts, srv, in)
		return status, data
	}
	return srv, w, call
}

func TestHubEndpointShapeAndTypes(t *testing.T) {
	_, w, call := hubWorld(t)
	status, data := call(request{method: "GET", path: "/api/hub"})
	mustStatus(t, status, 200, data)
	body := decodeMap(t, data)
	for _, key := range []string{"generated_at", "gate", "mailbox", "recent_answers"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("hub misses %q: %s", key, data)
		}
	}
	gate, _ := body["gate"].(map[string]any)
	labels, _ := gate["labels"].(map[string]any)
	if gate["open"] != false || gate["state"] != string(learning.Stuck) || labels["manual"] != float64(2) ||
		labels["judge"] != float64(3) || labels["needed"] != float64(10) || labels["review"] != float64(3) {
		t.Fatalf("gate %#v", gate)
	}
	recent, _ := body["recent_answers"].([]any)
	if len(recent) != 2 {
		t.Fatalf("recent %#v", recent)
	}
	first, _ := recent[0].(map[string]any)
	second, _ := recent[1].(map[string]any)
	for _, answer := range []map[string]any{first, second} {
		if _, ok := answer["type"]; ok {
			t.Fatalf("the hub answer duplicates the question catalogue: %#v", answer)
		}
	}
	for _, key := range []string{"body", "sender", "sender_address", "snippet"} {
		if strings.Contains(string(data), `"`+key+`"`) {
			t.Fatalf("hub leaked content field %q: %s", key, data)
		}
	}
	if w.hub.hubCalls != 1 || w.triage.questionCalls != 0 {
		t.Fatalf("calls hub %d questions %d, want 1/0", w.hub.hubCalls, w.triage.questionCalls)
	}
}

func TestAnswersEndpointShapeFilters(t *testing.T) {
	_, w, call := hubWorld(t)
	status, data := call(request{method: "GET", path: "/api/answers"})
	mustStatus(t, status, 200, data)
	body := decodeMap(t, data)
	items, _ := body["items"].([]any)
	if len(items) != 2 || body["next_cursor"] != float64(11) || body["has_more"] != true {
		t.Fatalf("answers %s", data)
	}
	first, _ := items[0].(map[string]any)
	second, _ := items[1].(map[string]any)
	for _, answer := range []map[string]any{first, second} {
		if _, ok := answer["type"]; ok {
			t.Fatalf("the answers row duplicates the question catalogue: %#v", answer)
		}
	}
	if w.hub.lastQuery != (hub.AnswerQuery{Limit: store.AnswerDefaultLimit}) {
		t.Fatalf("default query %+v", w.hub.lastQuery)
	}
	for _, key := range []string{"body", "sender", "sender_address", "snippet", "subject"} {
		if strings.Contains(string(data), `"`+key+`"`) {
			t.Fatalf("answers leaked content field %q: %s", key, data)
		}
	}

	status, data = call(request{method: "GET", path: "/api/answers?question=category&source=judge&message=7&cursor=99&limit=3&ignored=1"})
	mustStatus(t, status, 200, data)
	want := hub.AnswerQuery{Question: "category", Source: store.LabelJudge, MessageID: 7, Cursor: 99, Limit: 3}
	if w.hub.lastQuery != want {
		t.Fatalf("query %+v, want %+v", w.hub.lastQuery, want)
	}
	status, _ = call(request{method: "GET", path: "/api/answers?source=MANUAL&limit=100"})
	mustStatus(t, status, 200, nil)
	if w.hub.lastQuery.Source != store.LabelManual || w.hub.lastQuery.Limit != 100 {
		t.Fatalf("case/limit query %+v", w.hub.lastQuery)
	}
}

// The answer history carries the label value, never the triage read's
// internal distribution diagnostics or a second copy of the question type.
func TestAnswersCarryNoTriageDiagnostics(t *testing.T) {
	_, _, call := hubWorld(t)
	status, data := call(request{method: "GET", path: "/api/answers"})
	mustStatus(t, status, 200, data)
	requireNoTriageDiagnostics(t, data)
	body := decodeMap(t, data)
	items, _ := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("answers %s", data)
	}
	first, _ := items[0].(map[string]any)
	if first["question"] != "category" || first["value"] != "work" {
		t.Fatalf("the answer fields changed: %v", first)
	}
	if _, ok := first["type"]; ok {
		t.Fatalf("the answer row carries a duplicated type: %v", first)
	}
}

func TestAnswersValidation(t *testing.T) {
	_, w, call := hubWorld(t)
	cases := []struct {
		path string
		code string
	}{
		{"/api/answers?source=route", "invalid_source"},
		{"/api/answers?source=manual,judge", "invalid_source"},
		{"/api/answers?limit=0", "invalid_limit"},
		{"/api/answers?limit=-1", "invalid_limit"},
		{"/api/answers?limit=101", "invalid_limit"},
		{"/api/answers?limit=abc", "invalid_limit"},
		{"/api/answers?message=0", "invalid_message"},
		{"/api/answers?message=-4", "invalid_message"},
		{"/api/answers?message=abc", "invalid_message"},
		{"/api/answers?cursor=0", "invalid_cursor"},
		{"/api/answers?cursor=-2", "invalid_cursor"},
		{"/api/answers?cursor=abc", "invalid_cursor"},
	}
	before := w.hub.answerCalls
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			status, data := call(request{method: "GET", path: tc.path})
			mustStatus(t, status, 400, data)
			if code := errorCode(t, data); code != tc.code {
				t.Fatalf("code %q, want %q: %s", code, tc.code, data)
			}
		})
	}
	if w.hub.answerCalls != before {
		t.Fatalf("invalid queries reached the service: %d calls", w.hub.answerCalls-before)
	}
	status, data := call(request{method: "GET", path: "/api/answers?limit=0&ignored=1"})
	mustStatus(t, status, 400, data)
	if code := errorCode(t, data); code != "invalid_limit" {
		t.Fatalf("ignored parameter changed validation: %s", data)
	}
}

func TestLabelSummaryEndpointShape(t *testing.T) {
	_, w, call := hubWorld(t)
	status, data := call(request{method: "GET", path: "/api/labels/summary"})
	mustStatus(t, status, 200, data)
	body := decodeMap(t, data)
	if body["manual"] != float64(2) || body["judge"] != float64(3) || body["needed"] != float64(10) ||
		body["review"] != float64(3) {
		t.Fatalf("summary %s", data)
	}
	questions, _ := body["questions"].([]any)
	if len(questions) != 1 {
		t.Fatalf("summary %s", data)
	}
	if _, ok := body["blockers"]; ok {
		t.Fatalf("the label summary duplicates the gate blockers: %s", data)
	}
	for _, key := range []string{"mailbox", "recent_answers", "generated_at"} {
		if strings.Contains(string(data), `"`+key+`"`) {
			t.Fatalf("summary carries %q: %s", key, data)
		}
	}
	if w.hub.summaryCall != 1 {
		t.Fatalf("summary calls %d", w.hub.summaryCall)
	}
}

func TestHubEndpointsArePreGate(t *testing.T) {
	_, w, call := hubWorld(t)
	w.learning.allowErr = lockedGate()
	for _, path := range []string{"/api/hub", "/api/answers", "/api/labels/summary"} {
		status, data := call(request{method: "GET", path: path})
		mustStatus(t, status, 200, data)
	}
	if w.learning.calls != 0 {
		t.Fatalf("a hub read asked the learning gate: %d calls", w.learning.calls)
	}
}

func TestHubEndpointsRequireAuthAndMethod(t *testing.T) {
	srv, ts, _ := newTestServer(t)
	for _, path := range []string{"/api/hub", "/api/answers", "/api/labels/summary"} {
		status, _, data := do(t, ts, srv, request{method: "GET", path: path, skipAuth: true})
		mustStatus(t, status, 401, data)
		status, _, data = do(t, ts, srv, request{method: "POST", path: path})
		mustStatus(t, status, 405, data)
	}
}
