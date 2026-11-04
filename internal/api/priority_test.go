package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/priority"
	"github.com/LLM-4-People/mailsense/internal/qa"
	"github.com/LLM-4-People/mailsense/internal/subject"
)

func TestPriorityAuthAndGate(t *testing.T) {
	srv, ts, w := newTestServer(t)
	status, _, data := do(t, ts, srv, noAuth(request{method: "GET", path: "/api/priority"}))
	mustStatus(t, status, http.StatusUnauthorized, data)
	if w.priority.calls != 0 {
		t.Fatal("an unauthenticated request reached the service")
	}
	w.learning.allowErr = lockedGate()
	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/priority"})
	mustStatus(t, status, http.StatusLocked, data)
	if code := errorCode(t, data); code != "learning_locked" {
		t.Fatalf("code %q", code)
	}
	if w.priority.calls != 0 {
		t.Fatal("a locked gate reached the priority service")
	}
}

func TestPriorityPageShapeAndValidation(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.priority.items = []priority.Item{{
		MessageID: 4, Subject: "Budget", SenderName: "Alice", SenderAddress: "alice@example.com",
		ReceivedAt: "2026-10-25T10:00:00Z", FolderRole: "inbox", HasAttachments: true, Snippet: "please approve",
		Score: 87.5, Reasons: []string{"security", "urgent"}, Preview: true,
		Signals: priority.Signals{Urgency: 3, ReplyNeeded: 1, Suspicious: 1, DecisionNeeded: 0, Deadline: 4,
			Effort: 2, RequestCount: 2, Category: "security", AsksFor: "approval_decision",
			Subject: subject.Subject{Topic: "billing", Subtopic: "invoice"}},
	}}
	w.priority.total, w.priority.preview = 42, true
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/priority?limit=3&offset=2&filter=security"})
	mustStatus(t, status, http.StatusOK, data)
	if w.priority.lastFilter != priority.FilterSecurity || w.priority.lastLimit != 3 || w.priority.lastOffset != 2 {
		t.Fatalf("service args %s/%d/%d", w.priority.lastFilter, w.priority.lastLimit, w.priority.lastOffset)
	}
	body := decodeMap(t, data)
	if body["total"].(float64) != 42 || body["preview"] != true || body["generated_at"] == "" {
		t.Fatalf("view %#v", body)
	}
	if body["offset"].(float64) != 2 || body["available"].(float64) != 42 || body["has_more"] != true {
		t.Fatalf("page bounds %#v", body)
	}
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items %#v", body["items"])
	}
	item := items[0].(map[string]any)
	for _, key := range []string{"message_id", "subject", "sender_name", "sender_address", "received_at",
		"has_attachments", "snippet", "reasons", "preview", "signals", "action"} {
		if _, ok := item[key]; !ok {
			t.Errorf("item lacks %q: %#v", key, item)
		}
	}
	// The rank row is a screen contract, not the domain item: the score,
	// the folder role and the other classifier signals are server-side only.
	for _, banned := range []string{"body", "body_html", "message", "html", "score", "folder_role", "conversation_id"} {
		if _, ok := item[banned]; ok {
			t.Errorf("item leaks %q", banned)
		}
	}
	signals := item["signals"].(map[string]any)
	subjectSignals, _ := signals["subject"].(map[string]any)
	if len(signals) != 1 || subjectSignals["topic"] != "billing" || subjectSignals["subtopic"] != "invoice" {
		t.Fatalf("signals %#v", signals)
	}
	if _, ok := signals["urgency"]; ok {
		t.Fatalf("a dropped signal crossed the API: %#v", signals)
	}
	if strings.Contains(string(data), "<") {
		t.Fatalf("priority response contains markup: %s", data)
	}

	// Defaults.
	do(t, ts, srv, request{method: "GET", path: "/api/priority"})
	if w.priority.lastFilter != priority.FilterAll || w.priority.lastLimit != priority.DefaultLimit {
		t.Fatalf("defaults %s/%d", w.priority.lastFilter, w.priority.lastLimit)
	}

	w.priority.calls = 0
	for _, path := range []string{"/api/priority?filter=bogus", "/api/priority?limit=0", "/api/priority?limit=51",
		"/api/priority?limit=x", "/api/priority?offset=-1", "/api/priority?offset=x"} {
		status, _, data := do(t, ts, srv, request{method: "GET", path: path})
		mustStatus(t, status, http.StatusBadRequest, data)
		code := errorCode(t, data)
		if !strings.HasPrefix(code, "invalid_") {
			t.Fatalf("%s: code %q", path, code)
		}
	}
	if w.priority.calls != 0 {
		t.Fatalf("invalid queries reached the service %d times", w.priority.calls)
	}

	// An empty page is an array, never null.
	w.priority.items = nil
	_, _, data = do(t, ts, srv, request{method: "GET", path: "/api/priority"})
	body = decodeMap(t, data)
	if items, ok := body["items"].([]any); !ok || items == nil || len(items) != 0 {
		t.Fatalf("empty items %#v", body["items"])
	}
	// The shape is stable under a second encode.
	var again PriorityView
	if err := json.Unmarshal(data, &again); err != nil || again.Items == nil {
		t.Fatalf("decode %v", err)
	}
}

func TestAskEndpointAndValidation(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.ask.view = qa.AskView{ID: 7, Question: "what is urgent?", Answer: "", Mode: qa.ModeStructured,
		Intent: qa.IntentUrgent, Counts: map[string]int{"urgent": 2, "total": 9},
		Citations: []qa.Citation{{MessageID: 3, Subject: "s", SenderName: "a", ReceivedAt: "2026-10-25T10:00:00Z", Reason: "urgent"}},
		Provider:  "", Model: "", TookMS: 4, Preview: true, CreatedAt: "2026-10-26T12:00:00Z"}
	status, _, data := do(t, ts, srv, request{method: "POST", path: "/api/ask",
		body: `{"question":"what is urgent?","language":"en"}`})
	mustStatus(t, status, http.StatusOK, data)
	if w.ask.question != "what is urgent?" || w.ask.language != "en" {
		t.Fatalf("service args %q/%q", w.ask.question, w.ask.language)
	}
	body := decodeMap(t, data)
	if body["mode"] != qa.ModeStructured || body["intent"] != qa.IntentUrgent || body["id"].(float64) != 7 {
		t.Fatalf("view %#v", body)
	}
	if _, ok := body["notice"]; ok {
		t.Fatalf("an empty notice was encoded: %#v", body)
	}

	// The handler maps the service's typed errors to the contract codes.
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{qa.ErrInvalidQuestion, http.StatusBadRequest, "invalid_question"},
		{qa.ErrInvalidLanguage, http.StatusBadRequest, "invalid_language"},
		{qa.ErrInvalidConversation, http.StatusBadRequest, "invalid_conversation_id"},
		{qa.ErrDisabled, http.StatusServiceUnavailable, "qa_disabled"},
	} {
		w.ask.err = tc.err
		status, _, data := do(t, ts, srv, request{method: "POST", path: "/api/ask", body: `{"question":"hello"}`})
		mustStatus(t, status, tc.status, data)
		if code := errorCode(t, data); code != tc.code {
			t.Fatalf("%v: code %q", tc.err, code)
		}
	}
	w.ask.err = nil
	for _, body := range []string{"", `{`, `{"question":1}`, `{"question":"hello","extra":1}`} {
		status, _, data := do(t, ts, srv, request{method: "POST", path: "/api/ask", body: body})
		mustStatus(t, status, http.StatusBadRequest, data)
	}

	// Authentication and the learning gate.
	status, _, data = do(t, ts, srv, noAuth(request{method: "POST", path: "/api/ask", body: `{"question":"hello"}`}))
	mustStatus(t, status, http.StatusUnauthorized, data)
	before := w.ask.calls
	w.learning.allowErr = lockedGate()
	status, _, data = do(t, ts, srv, request{method: "POST", path: "/api/ask", body: `{"question":"hello"}`})
	mustStatus(t, status, http.StatusLocked, data)
	if w.ask.calls != before {
		t.Fatalf("a locked request reached the service (%d -> %d calls)", before, w.ask.calls)
	}
}

func TestAskLanguageNoticeWire(t *testing.T) {
	srv, ts, w := newTestServer(t)
	const answer = "The invoice was approved by the finance team and the payment will follow this week [1]."
	w.ask.view = qa.AskView{ID: 9, Question: "Que s'est-il passé avec la facture ?", Answer: answer,
		Mode: qa.ModeGenerated, Intent: qa.IntentFreeform, Notice: qa.NoticeAnswerLanguage,
		Provider: "fake", Model: "fake-model",
		Citations: []qa.Citation{{MessageID: 1, Subject: "Facture", SenderName: "Nadia",
			ReceivedAt: "2026-10-25T10:00:00Z", Reason: "retrieved"}}}
	status, _, data := do(t, ts, srv, request{method: "POST", path: "/api/ask",
		body: `{"question":"Que s'est-il passé avec la facture ?","language":"fr"}`})
	mustStatus(t, status, http.StatusOK, data)
	body := decodeMap(t, data)
	if body["notice"] != qa.NoticeAnswerLanguage {
		t.Fatalf("notice = %#v, want %q", body["notice"], qa.NoticeAnswerLanguage)
	}
	if got, _ := body["answer"].(string); got != answer {
		t.Fatalf("answer = %#v, want the noticed answer unchanged", body["answer"])
	}
	if body["mode"] != qa.ModeGenerated || body["provider"] != "fake" || body["model"] != "fake-model" {
		t.Fatalf("the noticed answer lost its mode or provider: %#v", body)
	}
}

func TestAskConversationIDRoundTrip(t *testing.T) {
	srv, ts, w := newTestServer(t)
	const conversationID = "0123456789abcdef"
	w.ask.view = qa.AskView{ID: 8, ConversationID: conversationID, Question: "première question",
		Mode: qa.ModeGenerated, Intent: qa.IntentFreeform, Counts: map[string]int{}, Citations: []qa.Citation{}}

	// The first turn of a new thread sends no id and receives the generated
	// one, which the UI keeps for every follow-up.
	status, _, data := do(t, ts, srv, request{method: "POST", path: "/api/ask",
		body: `{"question":"première question","language":"fr"}`})
	mustStatus(t, status, http.StatusOK, data)
	if w.ask.conversationID != "" {
		t.Fatalf("the initial question sent a conversation id %q", w.ask.conversationID)
	}
	body := decodeMap(t, data)
	if body["conversation_id"] != conversationID {
		t.Fatalf("response conversation id %#v", body["conversation_id"])
	}

	status, _, data = do(t, ts, srv, request{method: "POST", path: "/api/ask",
		body: `{"question":"et pour l'autre ?","language":"fr","conversation_id":"` + conversationID + `"}`})
	mustStatus(t, status, http.StatusOK, data)
	if w.ask.conversationID != conversationID {
		t.Fatalf("follow-up conversation id %q", w.ask.conversationID)
	}
	body = decodeMap(t, data)
	if body["conversation_id"] != conversationID {
		t.Fatalf("follow-up response conversation id %#v", body["conversation_id"])
	}

	w.ask.err = qa.ErrInvalidConversation
	status, _, data = do(t, ts, srv, request{method: "POST", path: "/api/ask",
		body: `{"question":"et pour l'autre ?","language":"fr","conversation_id":"bad id!"}`})
	mustStatus(t, status, http.StatusBadRequest, data)
	if w.ask.conversationID != "bad id!" {
		t.Fatalf("invalid id was not passed to the service: %q", w.ask.conversationID)
	}
	if code := errorCode(t, data); code != "invalid_conversation_id" {
		t.Fatalf("invalid conversation code %q", code)
	}
}

func TestAskQuestionEncodingIsTransparent(t *testing.T) {
	srv, ts, w := newTestServer(t)
	const question = "Résume le dernier échange avec messagerie-exemple.test"
	w.ask.view = qa.AskView{ID: 1, Question: question, Mode: qa.ModeGenerated, Intent: qa.IntentFreeform,
		Counts: map[string]int{}, Citations: []qa.Citation{}}

	status, _, data := do(t, ts, srv, request{method: "POST", path: "/api/ask",
		body: `{"question":"` + question + `","language":"fr"}`})
	mustStatus(t, status, http.StatusOK, data)
	if w.ask.question != question {
		t.Fatalf("raw UTF-8 question = %q, want %q", w.ask.question, question)
	}

	// A JSON producer may escape non-ASCII; it must decode to the same text.
	status, _, data = do(t, ts, srv, request{method: "POST", path: "/api/ask",
		body: `{"question":"R\u00e9sume le dernier \u00e9change avec messagerie-exemple.test","language":"fr"}`})
	mustStatus(t, status, http.StatusOK, data)
	if w.ask.question != question {
		t.Fatalf("escaped question = %q, want %q", w.ask.question, question)
	}

	// A body that is not UTF-8 is rejected at the boundary instead of being
	// silently turned into U+FFFD (encoding/json's behavior) and saved.
	before := w.ask.calls
	status, _, data = do(t, ts, srv, request{method: "POST", path: "/api/ask",
		body: "{\"question\":\"R\xe9sume le dernier \xe9change\",\"language\":\"fr\"}"})
	mustStatus(t, status, http.StatusBadRequest, data)
	if code := errorCode(t, data); code != "invalid_encoding" {
		t.Fatalf("invalid byte code %q", code)
	}
	if w.ask.calls != before {
		t.Fatalf("an invalid UTF-8 body reached the service (%d -> %d calls)", before, w.ask.calls)
	}
}

func TestAskHistoryAndClear(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.ask.history = []qa.HistoryRow{{ID: 2, Question: "q", Mode: qa.ModeRetrieval, CreatedAt: "2026-10-26T09:00:00Z"}}
	w.ask.historyItem = qa.AskView{ID: 2, Question: "q", Answer: "a", Mode: qa.ModeRetrieval,
		Notice: qa.NoticeUnavailable, Counts: map[string]int{}, Citations: []qa.Citation{}}
	w.ask.historyItemOK = true
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/ask/history"})
	mustStatus(t, status, http.StatusOK, data)
	if w.ask.lastLimit != qa.DefaultHistorySize {
		t.Fatalf("default history limit %d", w.ask.lastLimit)
	}
	body := decodeMap(t, data)
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("history items %#v", body["items"])
	}
	// The rail row is a projection: it never carries the answer payload.
	first, _ := items[0].(map[string]any)
	for _, key := range []string{"id", "question", "mode", "created_at"} {
		if _, ok := first[key]; !ok {
			t.Fatalf("history row misses %q: %#v", key, first)
		}
	}
	for _, key := range []string{"answer", "citations", "plan", "result", "agenda", "clarify"} {
		if _, ok := first[key]; ok {
			t.Fatalf("history row still carries %q: %#v", key, first)
		}
	}
	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/ask/history?limit=5"})
	mustStatus(t, status, http.StatusOK, data)
	if w.ask.lastLimit != 5 {
		t.Fatalf("history limit %d", w.ask.lastLimit)
	}

	// Opening a row is the one full read.
	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/ask/history/2"})
	mustStatus(t, status, http.StatusOK, data)
	if w.ask.historyItemCalls != 1 || w.ask.lastHistoryID != 2 {
		t.Fatalf("detail calls %d id %d", w.ask.historyItemCalls, w.ask.lastHistoryID)
	}
	detail := decodeMap(t, data)
	if detail["answer"] != "a" || detail["question"] != "q" {
		t.Fatalf("history detail %#v", detail)
	}
	for _, path := range []string{"/api/ask/history/0", "/api/ask/history/-1", "/api/ask/history/abc"} {
		status, _, data := do(t, ts, srv, request{method: "GET", path: path})
		mustStatus(t, status, http.StatusBadRequest, data)
		if code := errorCode(t, data); code != "invalid_id" {
			t.Fatalf("%s: code %q", path, code)
		}
	}
	if w.ask.historyItemCalls != 1 {
		t.Fatalf("invalid ids reached the service: %d calls", w.ask.historyItemCalls)
	}
	w.ask.historyItemOK = false
	status, _, data = do(t, ts, srv, request{method: "GET", path: "/api/ask/history/99"})
	mustStatus(t, status, http.StatusNotFound, data)

	for _, path := range []string{"/api/ask/history?limit=0", "/api/ask/history?limit=101", "/api/ask/history?limit=x"} {
		status, _, data := do(t, ts, srv, request{method: "GET", path: path})
		mustStatus(t, status, http.StatusBadRequest, data)
	}
	status, _, data = do(t, ts, srv, request{method: "DELETE", path: "/api/ask/history"})
	mustStatus(t, status, http.StatusNoContent, data)
	if w.ask.clearCalls != 1 {
		t.Fatalf("clear calls %d", w.ask.clearCalls)
	}
	if len(data) != 0 {
		t.Fatalf("204 carried a body: %q", data)
	}

	w.learning.allowErr = lockedGate()
	status, _, _ = do(t, ts, srv, request{method: "GET", path: "/api/ask/history"})
	if status != http.StatusLocked {
		t.Fatalf("history gate status %d", status)
	}
	status, _, _ = do(t, ts, srv, request{method: "GET", path: "/api/ask/history/2"})
	if status != http.StatusLocked {
		t.Fatalf("history detail gate status %d", status)
	}
	status, _, _ = do(t, ts, srv, request{method: "DELETE", path: "/api/ask/history"})
	if status != http.StatusLocked {
		t.Fatalf("clear gate status %d", status)
	}
}

// TestAskHistoryLimitUsesTheOneBoundedShape: a malformed limit and an
// out-of-range limit are the same parameter misuse, so they must produce the
// same ranged invalid_limit the client composes its localized message from.
func TestAskHistoryLimitUsesTheOneBoundedShape(t *testing.T) {
	srv, ts, _ := newTestServer(t)
	for _, path := range []string{
		"/api/ask/history?limit=abc",
		"/api/ask/history?limit=0",
		"/api/ask/history?limit=" + strconv.Itoa(qa.MaxHistorySize+1),
	} {
		status, _, data := do(t, ts, srv, request{method: "GET", path: path})
		mustStatus(t, status, http.StatusBadRequest, data)
		body := decodeMap(t, data)
		errObj, _ := body["error"].(map[string]any)
		if code, _ := errObj["code"].(string); code != "invalid_limit" {
			t.Fatalf("%s: code %q", path, code)
		}
		params, _ := errObj["params"].(map[string]any)
		if params == nil || params["min"] == nil || params["max"] == nil {
			t.Fatalf("%s: the refusal must carry its range, got %#v", path, errObj["params"])
		}
	}
}
