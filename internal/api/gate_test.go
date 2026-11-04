package api

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/layer1"
	"github.com/LLM-4-People/mailsense/internal/learning"
)

// lockedGate is a closed gate with one actionable blocker in each of two
// stages, the shape the UI renders.
func lockedGate() *learning.LockedError {
	return &learning.LockedError{Status: learning.Status{
		State: learning.Stuck,
		Stages: []learning.StageStatus{
			{ID: learning.StageMailboxes, State: learning.Blocked, Blockers: []learning.Blocker{{
				Code:    "no_mailboxes",
				Subject: "outlook",
				Detail:  "pick at least one mailbox",
				Actions: []learning.Action{{ID: learning.ActionPickMailboxes}},
			}}},
			{ID: learning.StageLabels, State: learning.Blocked, Blockers: []learning.Blocker{{
				Code:    "labels_missing",
				Subject: "category",
				Detail:  "label more messages",
				Actions: []learning.Action{{ID: learning.ActionReviewFailures, Params: map[string]string{"log": "layer1"}}},
				Measure: &learning.Measure{Have: 2, Need: 10, Unit: "labels"},
			}}},
		},
	}}
}

func TestGateRefusals(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.learning.allowErr = lockedGate()
	gated := []struct {
		name string
		in   request
	}{
		{"triage read", request{method: "GET", path: "/api/triage"}},
		{"triage result", request{method: "GET", path: "/api/triage/1"}},
		{"search", request{method: "GET", path: "/api/search?q=hello"}},
		{"messages", request{method: "GET", path: "/api/messages"}},
		{"message", request{method: "GET", path: "/api/messages/1"}},
		{"message attachment", request{method: "GET", path: "/api/messages/1/attachments/0"}},
		{"thread", request{method: "GET", path: "/api/threads/C1"}},
		{"conversations", request{method: "GET", path: "/api/conversations"}},
		{"commitments", request{method: "GET", path: "/api/commitments"}},
		{"brief", request{method: "GET", path: "/api/brief"}},
		{"companies", request{method: "GET", path: "/api/companies"}},
		{"company", request{method: "GET", path: "/api/companies/c%3Aatelier%20nord"}},
	}
	for _, tc := range gated {
		t.Run(tc.name, func(t *testing.T) {
			status, _, data := do(t, ts, srv, tc.in)
			mustStatus(t, status, 423, data)
			body := decodeMap(t, data)
			errBody, _ := body["error"].(map[string]any)
			if errBody["code"] != "learning_locked" {
				t.Fatalf("code %v", errBody["code"])
			}
			problems, _ := errBody["problems"].([]any)
			if len(problems) != 2 {
				t.Fatalf("problems %#v", problems)
			}
			first, _ := problems[0].(map[string]any)
			second, _ := problems[1].(map[string]any)
			if first["stage"] != learning.StageMailboxes || first["code"] != "no_mailboxes" {
				t.Fatalf("first problem %#v", first)
			}
			if len(first["actions"].([]any)) != 1 {
				t.Fatalf("first actions %#v", first["actions"])
			}
			measure, _ := second["measure"].(map[string]any)
			if measure["have"].(float64) != 2 || measure["need"].(float64) != 10 || measure["unit"] != "labels" {
				t.Fatalf("second measure %#v", measure)
			}
		})
	}
	if w.triage.readCalls != 0 || w.triage.resultCall != 0 || w.search.calls != 0 || w.messages.calls != 0 ||
		w.messages.listCalls != 0 ||
		w.threads.calls != 0 || w.conversations.calls != 0 || w.commitments.calls != 0 || w.brief.calls != 0 ||
		w.companies.listCalls != 0 || w.companies.profileCalls != 0 {
		t.Fatalf("a locked gate reached a service: triage %d/%d search %d messages %d/%d threads %d conversations %d commitments %d brief %d companies %d/%d",
			w.triage.readCalls, w.triage.resultCall, w.search.calls, w.messages.calls, w.messages.listCalls, w.threads.calls,
			w.conversations.calls, w.commitments.calls, w.brief.calls, w.companies.listCalls, w.companies.profileCalls)
	}
	// Labelling is what opens the gate, so the label screen may read the
	// message the queue offers before the gate opens; no other message read
	// may.
	code, _, data := do(t, ts, srv, request{method: "GET", path: "/api/messages/1?for=label&question=category&limit=20"})
	mustStatus(t, code, 200, data)
	if w.messages.calls != 1 || w.labels.containsCalls != 1 {
		t.Fatalf("a pre-gate label read did not reach the message and queue owners: %d/%d", w.messages.calls, w.labels.containsCalls)
	}
	// A message the queue does not offer stays behind the gate even with
	// the label flag: the flag alone is not an unlock.
	code, _, data = do(t, ts, srv, request{method: "GET", path: "/api/messages/2?for=label&question=category&limit=20"})
	mustStatus(t, code, 423, data)
	if w.messages.calls != 1 {
		t.Fatalf("an unoffered pre-gate label read reached the message service: %d", w.messages.calls)
	}
	// A label read without the queue binding is the gate error, not an
	// unlocked message.
	code, _, data = do(t, ts, srv, request{method: "GET", path: "/api/messages/1?for=label"})
	mustStatus(t, code, 423, data)
	if w.messages.calls != 1 {
		t.Fatalf("an unbound pre-gate label read reached the message service: %d", w.messages.calls)
	}
}

func TestGateRefusalKeepsLearningOpen(t *testing.T) {
	srv, ts, w := newTestServer(t)
	status := lockedGate().Status
	w.learning.allowErr = &learning.LockedError{Status: status}
	w.learning.status = status
	w.learning.open = false
	code, _, data := do(t, ts, srv, request{method: "GET", path: "/api/learning"})
	mustStatus(t, code, 200, data)
	body := decodeMap(t, data)
	if open, ok := body["open"].(bool); !ok || open {
		t.Fatalf("open = %v", body["open"])
	}
	st, _ := body["status"].(map[string]any)
	if st["state"] != learning.Stuck {
		t.Fatalf("state %v", st["state"])
	}
}

func TestGateOpenLatch(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.learning.allowErr = nil
	w.learning.status = learning.Status{State: learning.Learning, Stages: []learning.StageStatus{
		{ID: learning.StageSync, State: learning.Running, Note: "learning new mail"},
	}}
	w.learning.open = true
	code, _, data := do(t, ts, srv, request{method: "GET", path: "/api/learning"})
	mustStatus(t, code, 200, data)
	body := decodeMap(t, data)
	if open, ok := body["open"].(bool); !ok || !open {
		t.Fatalf("open = %v: the latch must keep the gate open while learning continues", body["open"])
	}
	st, _ := body["status"].(map[string]any)
	if st["state"] != learning.Learning {
		t.Fatalf("state %v", st["state"])
	}
}

func TestGateRace(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.learning.allowErr = nil // the middleware's check passes…
	w.triage.pageErr = lockedGate()
	w.triage.resultErr = lockedGate()
	w.search.err = lockedGate()
	w.messages.err = lockedGate()

	w.threads.err = lockedGate()
	w.conversations.err = lockedGate()
	w.commitments.err = lockedGate()
	w.brief.err = lockedGate()
	w.companies.listErr = lockedGate()
	w.companies.profileErr = lockedGate()
	for _, in := range []request{
		{method: "GET", path: "/api/triage"},
		{method: "GET", path: "/api/triage/1"},
		{method: "GET", path: "/api/search?q=hello"},
		{method: "GET", path: "/api/messages"},
		{method: "GET", path: "/api/messages/1?for=label"},
		{method: "GET", path: "/api/threads/C1"},
		{method: "GET", path: "/api/conversations"},
		{method: "GET", path: "/api/commitments"},
		{method: "GET", path: "/api/brief"},
		{method: "GET", path: "/api/companies"},
		{method: "GET", path: "/api/companies/c%3Aatelier%20nord"},
	} {
		status, _, data := do(t, ts, srv, in)
		mustStatus(t, status, 423, data)
		if code := errorCode(t, data); code != "learning_locked" {
			t.Fatalf("%s: code %q", in.path, code)
		}
	}
}

func TestGateFailureIsNotLock(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.learning.allowErr = errors.New("gate unavailable")
	code, _, data := do(t, ts, srv, request{method: "GET", path: "/api/triage"})
	mustStatus(t, code, 500, data)
	if got := errorCode(t, data); got != "internal" {
		t.Fatalf("code %q", got)
	}
	if w.triage.readCalls != 0 {
		t.Fatal("the service was reached through a failing gate")
	}
}

func TestPreGateEndpointsStayAvailable(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.learning.allowErr = lockedGate()
	cases := []struct {
		in     request
		status int
	}{
		{request{method: "GET", path: "/api/status"}, 200},
		{request{method: "GET", path: "/api/learning"}, 200},
		{request{method: "GET", path: "/api/settings"}, 200},
		{request{method: "GET", path: "/api/secrets"}, 200},
		{request{method: "GET", path: "/api/mailboxes"}, 200},
		{request{method: "GET", path: "/api/jobs"}, 200},
		{request{method: "POST", path: "/api/actions/sync", body: `{"source":"outlook"}`}, 202},
		{request{method: "POST", path: "/api/actions/run_evidence", body: `{}`}, 202},
		{request{method: "POST", path: "/api/actions/index", body: `{}`}, 202},
		{request{method: "GET", path: "/api/logs"}, 200},
		{request{method: "GET", path: "/api/sidecar"}, 200},
		{request{method: "GET", path: "/api/providers"}, 200},
		{request{method: "POST", path: "/api/providers/discover", body: `{"base_url":"https://provider.example.test/v1"}`}, 200},
		{request{method: "GET", path: "/api/labels/queue?question=category"}, 200},
		{request{method: "POST", path: "/api/labels", body: `{"items":[{"message_id":1,"question":"category","value":"work"}]}`}, 200},
	}
	for _, tc := range cases {
		t.Run(tc.in.path, func(t *testing.T) {
			status, _, data := do(t, ts, srv, tc.in)
			mustStatus(t, status, tc.status, data)
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp, _, cancelStream := streamRequest(t, ts, srv, "/api/events", true, ctx)
	defer cancelStream()
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("events status %d", resp.StatusCode)
	}
}

// TestGateAllowsWithoutReadingTheStatus is handler-level: on the open/latched
// path production Learning.Allow returns after reading the learned-scope latch
// and does not call Status, so the handler never builds the full status. A
// locked gate is different: Allow builds and returns the status carried by
// TestGateReturnsTheLockedStatus. This fake proves only that the handler does
// not add a Status call of its own.
func TestGateAllowsWithoutReadingTheStatus(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.learning.allowErr = nil
	w.learning.status = learning.Status{State: learning.Learned}
	w.learning.open = true

	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/gate"})
	mustStatus(t, status, 200, data)
	body := decodeMap(t, data)
	if allowed, ok := body["allowed"].(bool); !ok || !allowed {
		t.Fatalf("allowed = %v", body["allowed"])
	}
	if _, present := body["status"]; present {
		t.Fatalf("an open gate carried a status: %#v", body["status"])
	}
	if w.learning.statusCalls != 0 {
		t.Fatalf("the gate read the full status %d times, want 0", w.learning.statusCalls)
	}
	if w.learning.calls != 1 {
		t.Fatalf("Allow called %d times, want 1", w.learning.calls)
	}
}

// A locked production Allow builds the full status before returning the typed
// lock; the handler must carry that status to the UI and still answer 200.
func TestGateReturnsTheLockedStatus(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.learning.allowErr = lockedGate()
	w.learning.preview = false

	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/gate"})
	mustStatus(t, status, 200, data)
	body := decodeMap(t, data)
	if allowed, ok := body["allowed"].(bool); !ok || allowed {
		t.Fatalf("allowed = %v, want false", body["allowed"])
	}
	if preview, ok := body["preview"].(bool); !ok || preview {
		t.Fatalf("preview = %v, want false", body["preview"])
	}
	st, ok := body["status"].(map[string]any)
	if !ok {
		t.Fatalf("locked gate has no status: %s", data)
	}
	if st["state"] != learning.Stuck {
		t.Fatalf("state = %v, want %s", st["state"], learning.Stuck)
	}
	stages, _ := st["stages"].([]any)
	if len(stages) != 2 {
		t.Fatalf("locked status has %d stages, want the two blockers", len(stages))
	}
	if w.learning.statusCalls != 0 {
		t.Fatalf("the gate read the full status %d times, want 0", w.learning.statusCalls)
	}
}

func TestGateReportsPreview(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.learning.allowErr = nil
	w.learning.open = false
	w.learning.preview = true

	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/gate"})
	mustStatus(t, status, 200, data)
	body := decodeMap(t, data)
	if allowed, ok := body["allowed"].(bool); !ok || !allowed {
		t.Fatalf("allowed = %v", body["allowed"])
	}
	if preview, ok := body["preview"].(bool); !ok || !preview {
		t.Fatalf("preview = %v, want true", body["preview"])
	}
}

// A real gate failure is an error, not a locked answer; a wrong method and
// a missing token keep the normal transport rules.
func TestGateTransport(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.learning.allowErr = errors.New("gate unavailable")

	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/gate"})
	mustStatus(t, status, 500, data)
	if got := errorCode(t, data); got != "internal" {
		t.Fatalf("code %q", got)
	}

	status, header, data := do(t, ts, srv, request{method: "POST", path: "/api/gate"})
	mustStatus(t, status, 405, data)
	if allow := header.Get("Allow"); allow != "GET" {
		t.Fatalf("Allow %q, want GET", allow)
	}

	status, _, data = do(t, ts, srv, noAuth(request{method: "GET", path: "/api/gate"}))
	mustStatus(t, status, 401, data)
}

func TestPreGateReadsAreBoundToTheirSurface(t *testing.T) {
	// A non-lock Allow failure is an ordinary server error. It must never be
	// mistaken for a lock a query flag can bypass.
	srv, ts, w := newTestServer(t)
	w.learning.allowErr = errors.New("boom")
	code, _, data := do(t, ts, srv, request{method: "GET", path: "/api/messages/1?for=label&question=category&limit=20"})
	mustStatus(t, code, 500, data)
	if w.messages.calls != 0 {
		t.Fatalf("a failed gate read reached the message service: %d", w.messages.calls)
	}

	// A queue that cannot prove membership answers the gate error, not its
	// own failure: the caller learns only that the message is not readable.
	srv, ts, w = newTestServer(t)
	w.learning.allowErr = lockedGate()
	w.labels.queueErr = errors.New("queue down")
	code, _, data = do(t, ts, srv, request{method: "GET", path: "/api/messages/1?for=label&question=category&limit=20"})
	mustStatus(t, code, 423, data)
	if got := errorCode(t, data); got != "learning_locked" {
		t.Fatalf("queue failure code %q", got)
	}
	if w.messages.calls != 0 {
		t.Fatalf("a failed queue proof reached the message service: %d", w.messages.calls)
	}

	// The Answers screen has its own surface: the stored answer binds the
	// read. An answered message opens; an unanswered one and the bare label
	// flag do not.
	srv, ts, w = newTestServer(t)
	w.learning.allowErr = lockedGate()
	code, _, data = do(t, ts, srv, request{method: "GET", path: "/api/messages/1?for=answer"})
	mustStatus(t, code, 200, data)
	if w.messages.calls != 1 || w.hub.lastQuery.MessageID != 1 || w.hub.lastQuery.Limit != 1 {
		t.Fatalf("answer read: messages %d query %+v", w.messages.calls, w.hub.lastQuery)
	}
	code, _, data = do(t, ts, srv, request{method: "GET", path: "/api/messages/2?for=answer"})
	mustStatus(t, code, 423, data)
	if w.messages.calls != 1 {
		t.Fatalf("an unanswered pre-gate read reached the message service: %d", w.messages.calls)
	}
	code, _, data = do(t, ts, srv, request{method: "GET", path: "/api/messages/1?for=label"})
	mustStatus(t, code, 423, data)
	if w.messages.calls != 1 {
		t.Fatalf("a bare label flag reached the message service: %d", w.messages.calls)
	}
}

// TestPreGateLabelBindingUsesTheQueueLimitBound: the pre-gate binding is the
// same judgement the label queue route makes, so it must apply that route's
// limit bound. An out-of-range or malformed limit used to reach the queue
// owner anyway (or was silently discarded), which made one parameter obey two
// different rules. Every failure still answers the gate error, so the caller
// learns nothing beyond "not readable yet".
func TestPreGateLabelBindingUsesTheQueueLimitBound(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.learning.allowErr = lockedGate()
	for _, suffix := range []string{
		"&limit=abc",
		"&limit=-1",
		"&limit=" + strconv.Itoa(layer1.LabelQueueMax+1),
	} {
		code, _, data := do(t, ts, srv, request{method: "GET",
			path: "/api/messages/1?for=label&question=category" + suffix})
		mustStatus(t, code, 423, data)
	}
	if w.labels.containsCalls != 0 {
		t.Fatalf("an out-of-range limit reached the queue owner %d times", w.labels.containsCalls)
	}
	if w.messages.calls != 0 {
		t.Fatalf("an unbound pre-gate label read reached the message service %d times", w.messages.calls)
	}
	// The valid bound still opens the one message the queue offers.
	code, _, data := do(t, ts, srv, request{method: "GET",
		path: "/api/messages/1?for=label&question=category&limit=" + strconv.Itoa(layer1.LabelQueueMax)})
	mustStatus(t, code, 200, data)
	if w.labels.containsCalls != 1 || w.messages.calls != 1 {
		t.Fatalf("the valid binding did not reach the owners: %d/%d", w.labels.containsCalls, w.messages.calls)
	}
}

// TestLearningViewReadsTheGateOnce: /api/learning used to ask the gate twice
// (a status read that resolved Open, then the preview rule which resolves Open
// again). One reading now answers the status, the gate and the preview, so the
// screen cannot paint a status from one state beside a gate from another.
func TestLearningViewReadsTheGateOnce(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.learning.status = learning.Status{State: learning.Learned}
	w.learning.open = true
	before := w.learning.calls

	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/learning"})
	mustStatus(t, status, 200, data)
	if got := w.learning.calls - before; got != 1 {
		t.Fatalf("the learning view read the gate %d times, want 1", got)
	}
	body := decodeMap(t, data)
	if body["open"] != true || body["preview"] != false {
		t.Fatalf("learning view %#v", body)
	}
}
