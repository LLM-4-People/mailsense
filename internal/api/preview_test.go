package api

import (
	"testing"

	"github.com/LLM-4-People/mailsense/internal/learning"
)

// The learning and gate views publish the effective preview rule: the raw
// setting alone is not preview while the real gate is open.
func TestLearningViewsReportTheEffectivePreviewRule(t *testing.T) {
	for _, tc := range []struct {
		name          string
		setting, open bool
		want          bool
	}{
		{"setting on, gate open", true, true, false},
		{"setting on, gate closed", true, false, true},
		{"setting off, gate closed", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, ts, w := newTestServer(t)
			w.learning.allowErr = nil
			w.learning.preview = tc.setting
			w.learning.open = tc.open

			for _, path := range []string{"/api/learning", "/api/gate"} {
				status, _, data := do(t, ts, srv, request{method: "GET", path: path})
				mustStatus(t, status, 200, data)
				body := decodeMap(t, data)
				if preview, ok := body["preview"].(bool); !ok || preview != tc.want {
					t.Fatalf("%s preview = %v, want %v", path, body["preview"], tc.want)
				}
			}
		})
	}
}

// The explicit test preview unlocks the gated answer reads, but the
// learning view keeps reporting the real gate: open stays false, the
// status is unchanged and only the separate preview flag is true.
func TestLearningPreviewUnlocksReadsButNeverTheGate(t *testing.T) {
	t.Run("preview on", func(t *testing.T) {
		srv, ts, w := newTestServer(t)
		w.learning.allowErr = nil
		w.learning.open = false
		w.learning.preview = true
		w.learning.status = learning.Status{State: learning.Stuck, Stages: []learning.StageStatus{
			{ID: learning.StageLabels, State: learning.Blocked},
		}}

		code, _, data := do(t, ts, srv, request{method: "GET", path: "/api/learning"})
		mustStatus(t, code, 200, data)
		body := decodeMap(t, data)
		if open, ok := body["open"].(bool); !ok || open {
			t.Fatalf("open = %v: the preview must not open the real gate", body["open"])
		}
		if preview, ok := body["preview"].(bool); !ok || !preview {
			t.Fatalf("preview = %v", body["preview"])
		}
		st, _ := body["status"].(map[string]any)
		if st["state"] != learning.Stuck {
			t.Fatalf("status changed by the preview: %v", st["state"])
		}

		// /api/status is not a second learning view: the gate has one
		// endpoint, and the status poll no longer pays a second
		// Learning.Status read for a field no screen reads.
		code, _, data = do(t, ts, srv, request{method: "GET", path: "/api/status"})
		mustStatus(t, code, 200, data)
		if _, ok := decodeMap(t, data)["learning"]; ok {
			t.Fatal("/api/status still embeds the learning view")
		}

		// The gated reads reach their services through the machine's Allow.
		for _, in := range []request{
			{method: "GET", path: "/api/triage"},
			{method: "GET", path: "/api/triage/summary"},
			{method: "GET", path: "/api/search?q=hello"},
			{method: "GET", path: "/api/messages"},
			{method: "GET", path: "/api/messages/1"},
			{method: "GET", path: "/api/conversations"},
			{method: "GET", path: "/api/commitments"},
			{method: "GET", path: "/api/brief"},
		} {
			code, _, data := do(t, ts, srv, in)
			mustStatus(t, code, 200, data)
		}
		if w.triage.readCalls != 1 || w.triage.summaryCalls != 1 || w.search.calls != 1 || w.messages.calls != 1 ||
			w.messages.listCalls != 1 ||
			w.conversations.calls != 1 || w.commitments.calls != 1 || w.brief.calls != 1 {
			t.Fatalf("preview did not reach every gated read: triage %d/%d search %d messages %d/%d conversations %d commitments %d brief %d",
				w.triage.readCalls, w.triage.summaryCalls, w.search.calls, w.messages.calls, w.messages.listCalls, w.conversations.calls,
				w.commitments.calls, w.brief.calls)
		}
	})

	t.Run("preview off", func(t *testing.T) {
		srv, ts, w := newTestServer(t)
		w.learning.allowErr = lockedGate()
		w.learning.open = false
		w.learning.preview = false
		w.learning.status = lockedGate().Status

		code, _, data := do(t, ts, srv, request{method: "GET", path: "/api/learning"})
		mustStatus(t, code, 200, data)
		body := decodeMap(t, data)
		if preview, ok := body["preview"].(bool); !ok || preview {
			t.Fatalf("preview = %v", body["preview"])
		}
		if open, _ := body["open"].(bool); open {
			t.Fatalf("open = %v", body["open"])
		}

		code, _, data = do(t, ts, srv, request{method: "GET", path: "/api/triage"})
		mustStatus(t, code, 423, data)
		if code := errorCode(t, data); code != "learning_locked" {
			t.Fatalf("code %q", code)
		}
		if w.triage.readCalls != 0 {
			t.Fatalf("a closed preview reached triage: %d", w.triage.readCalls)
		}
	})
}
