package api

import (
	"strconv"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/feedback"
	"github.com/LLM-4-People/mailsense/internal/layer1"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// Review is an explicit queue flag, and a review task carries the decoded
// judge suggestion (including a false yes/no value) without message content.
func TestLabelQueueReviewFlagAndSuggestion(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.labels.queue = layer1.LabelQueue{
		Question: "category",
		Strategy: layer1.LabelStrategyMissing,
		Tasks: []layer1.LabelTask{{MessageID: 7, Question: "category", Reason: layer1.LabelReasonReview,
			Suggestion: "work", SuggestionSource: store.LabelJudge}},
	}
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/labels/queue?question=category&limit=3&review=true"})
	mustStatus(t, status, 200, data)
	if w.labels.lastQuery != (layer1.LabelQuery{Question: "category", Limit: 3, Review: true}) {
		t.Fatalf("query %+v", w.labels.lastQuery)
	}
	task := onlyTask(t, data)
	if task["suggestion"] != "work" || task["suggestion_source"] != store.LabelJudge || task["reason"] != layer1.LabelReasonReview {
		t.Fatalf("task %v", task)
	}
	for _, key := range []string{"subject", "body", "sender", "sender_address", "snippet", "preview"} {
		if _, leaked := task[key]; leaked {
			t.Fatalf("review task carries message field %q", key)
		}
	}

	// A no suggestion is false, not an omitted field: omitempty must not
	// swallow the value the answer controls apply.
	w.labels.queue = layer1.LabelQueue{
		Question: "reply_needed",
		Strategy: layer1.LabelStrategyUncertainty,
		Tasks: []layer1.LabelTask{{MessageID: 8, Question: "reply_needed", Reason: layer1.LabelReasonReview,
			Suggestion: false, SuggestionSource: store.LabelJudge}},
	}
	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/labels/queue?question=reply_needed&review=1"})
	mustStatus(t, status, 200, data)
	if w.labels.lastQuery != (layer1.LabelQuery{Question: "reply_needed", Review: true}) {
		t.Fatalf("query %+v", w.labels.lastQuery)
	}
	task = onlyTask(t, data)
	if value, has := task["suggestion"]; !has || value != false {
		t.Fatalf("a false suggestion is missing or invalid: %v", task)
	}

	// The normal queue does not ask for review and carries no suggestion.
	w.labels.queue = layer1.LabelQueue{
		Question: "category",
		Strategy: layer1.LabelStrategyMissing,
		Tasks:    []layer1.LabelTask{{MessageID: 9, Question: "category", Reason: layer1.LabelReasonMissing}},
	}
	for _, path := range []string{
		"/api/labels/queue?question=category",
		"/api/labels/queue?question=category&review=false",
		"/api/labels/queue?question=category&review=0",
		"/api/labels/queue?question=category&review=",
	} {
		status, _, data = do(t, ts, srv, request{method: "GET", path: path})
		mustStatus(t, status, 200, data)
		if w.labels.lastQuery.Review {
			t.Fatalf("%s asked for review", path)
		}
		task = onlyTask(t, data)
		if _, has := task["suggestion"]; has {
			t.Fatalf("%s returned a suggestion: %v", path, task)
		}
		if _, has := task["suggestion_source"]; has {
			t.Fatalf("%s returned a suggestion source: %v", path, task)
		}
	}
}

// The review flag keeps the queue's existing question/limit validation and
// rejects a value that is not a boolean spelling.
func TestLabelQueueReviewValidation(t *testing.T) {
	srv, ts, _ := newTestServer(t)
	cases := []struct {
		path   string
		status int
		code   string
	}{
		{"/api/labels/queue?review=true", 400, "invalid_request"},
		{"/api/labels/queue?question=category&limit=9999&review=true", 400, "invalid_limit"},
		{"/api/labels/queue?question=category&review=maybe", 400, "invalid_review"},
	}
	for _, tc := range cases {
		status, _, data := do(t, ts, srv, request{method: "GET", path: tc.path})
		mustStatus(t, status, tc.status, data)
		if code := errorCode(t, data); code != tc.code {
			t.Fatalf("%s: code %q, want %q", tc.path, code, tc.code)
		}
	}
}

// The label queue is an action surface, not a triage read: neither the
// normal queue nor review mode may carry the internal diagnostic fields.
func TestLabelQueueCarriesNoTriageDiagnostics(t *testing.T) {
	srv, ts, w := newTestServer(t)
	cases := []struct {
		name  string
		path  string
		queue layer1.LabelQueue
	}{
		{"normal", "/api/labels/queue?question=category", layer1.LabelQueue{
			Question: "category", Strategy: layer1.LabelStrategyMissing,
			Tasks: []layer1.LabelTask{{MessageID: 7, Question: "category", Reason: layer1.LabelReasonMissing}},
		}},
		{"review", "/api/labels/queue?question=category&review=true", layer1.LabelQueue{
			Question: "category", Strategy: layer1.LabelStrategyUncertainty,
			Tasks: []layer1.LabelTask{{MessageID: 8, Question: "category", Reason: layer1.LabelReasonReview,
				Suggestion: "work", SuggestionSource: store.LabelJudge}},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w.labels.queue = tc.queue
			status, _, data := do(t, ts, srv, request{method: "GET", path: tc.path})
			mustStatus(t, status, 200, data)
			requireNoTriageDiagnostics(t, data)
			task := onlyTask(t, data)
			if task["question"] != "category" || task["message_id"] != float64(tc.queue.Tasks[0].MessageID) ||
				task["reason"] != tc.queue.Tasks[0].Reason {
				t.Fatalf("the task fields changed: %v", task)
			}
		})
	}
}

func onlyTask(t *testing.T, data []byte) map[string]any {
	t.Helper()
	body := decodeMap(t, data)
	tasks, ok := body["tasks"].([]any)
	if !ok || len(tasks) != 1 {
		t.Fatalf("tasks %T %v", body["tasks"], body["tasks"])
	}
	task, ok := tasks[0].(map[string]any)
	if !ok {
		t.Fatalf("task %T", tasks[0])
	}
	return task
}

func TestLabelBatchValidationIsAProblem(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.labels.putErr = &feedback.ValidationError{Problems: []string{"message 9 does not exist or is not visible"}}
	status, _, data := do(t, ts, srv, request{method: "POST", path: "/api/labels",
		body: `{"items":[{"message_id":9,"question":"category","value":"work"}]}`})
	mustStatus(t, status, 422, data)
	if got := errorCode(t, data); got != "invalid_value" {
		t.Fatalf("code %q", got)
	}
	body := decodeMap(t, data)
	errBody, _ := body["error"].(map[string]any)
	problems, _ := errBody["problems"].([]any)
	if len(problems) != 1 {
		t.Fatalf("problems %#v", problems)
	}
	first, _ := problems[0].(map[string]any)
	if first["message"] != "message 9 does not exist or is not visible" {
		t.Fatalf("problem %#v", first)
	}
	if w.labels.putCalls != 1 {
		t.Fatalf("put calls %d", w.labels.putCalls)
	}
}

// The delete route answers the batch envelope and refuses only the path id;
// the labeler owns the question and message rules.
func TestLabelDeleteWireAndErrors(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.labels.del = feedback.PutResult{Stored: []feedback.StoredLabel{{MessageID: 7, Question: "category", Source: store.LabelManual}}}
	status, _, data := do(t, ts, srv, request{method: "DELETE", path: "/api/labels/7/category"})
	mustStatus(t, status, 200, data)
	if w.labels.deleteCalls != 1 || w.labels.lastDeleteID != 7 || w.labels.lastDeleteQ != "category" {
		t.Fatalf("delete call %d id %d question %q", w.labels.deleteCalls, w.labels.lastDeleteID, w.labels.lastDeleteQ)
	}
	body := decodeMap(t, data)
	stored, _ := body["stored"].([]any)
	if len(stored) != 1 {
		t.Fatalf("stored %#v", body["stored"])
	}
	item, _ := stored[0].(map[string]any)
	if item["message_id"] != float64(7) || item["question"] != "category" || item["source"] != store.LabelManual {
		t.Fatalf("stored item %#v", item)
	}

	cases := []struct {
		name   string
		path   string
		err    error
		status int
		code   string
	}{
		{"malformed id", "/api/labels/nope/category", nil, 400, "invalid_id"},
		{"zero id", "/api/labels/0/category", nil, 400, "invalid_id"},
		{"unknown question", "/api/labels/7/not_a_question", feedback.ErrUnknownLabelQuestion, 400, "invalid_question"},
		{"missing message", "/api/labels/7/category", store.ErrNotFound, 404, "not_found"},
	}
	before := w.labels.deleteCalls
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w.labels.delErr = tc.err
			status, _, data := do(t, ts, srv, request{method: "DELETE", path: tc.path})
			mustStatus(t, status, tc.status, data)
			if code := errorCode(t, data); code != tc.code {
				t.Fatalf("code %q, want %q", code, tc.code)
			}
		})
	}
	if w.labels.deleteCalls-before != 2 {
		t.Fatalf("a malformed path reached the service: %d calls", w.labels.deleteCalls-before)
	}
}

// Against the real store and labeler, a deleted label is gone from every
// read, and deleting it again is idempotent (the store's delete is).
func TestLabelDeleteRemovesTheStoredLabel(t *testing.T) {
	srv, ts, w, st, ids := labelWorld(t, 1)
	status, _, data := do(t, ts, srv, request{method: "POST", path: "/api/labels",
		body: `{"items":[{"message_id":` + jsonNumber(ids[0]) + `,"question":"category","value":"work"}]}`})
	mustStatus(t, status, 200, data)
	if n := labelCount(t, st); n != 1 {
		t.Fatalf("label stored %d", n)
	}
	path := "/api/labels/" + jsonNumber(ids[0]) + "/category"
	for range 2 {
		status, _, data := do(t, ts, srv, request{method: "DELETE", path: path})
		mustStatus(t, status, 200, data)
	}
	if n := labelCount(t, st); n != 0 {
		t.Fatalf("deleted label still listed: %d", n)
	}
	if w.labels.deleteCalls != 0 {
		t.Fatalf("the API bypassed the real labeler: %d fake calls", w.labels.deleteCalls)
	}
}

// jsonNumber renders a positive row id as the JSON literal the label batch
// takes, so the contract test never hand-formats one.
func jsonNumber(n int64) string {
	return strconv.FormatInt(n, 10)
}
