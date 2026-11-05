package api

import (
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/update"
)

// TestUpdateStatusIsLocal proves the GET is a state read: it answers the
// documented fields and never calls the check, so a screen refresh cannot
// spend the GitHub rate limit.
func TestUpdateStatusIsLocal(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.update.status = update.Status{
		Current:        "0.1.0",
		Latest:         "0.2.0",
		Available:      true,
		VersionsBehind: 2,
		ReleaseURL:     "https://github.com/LLM-4-People/mailsense/releases/tag/v0.2.0",
		Notes:          "The release notes",
		NotesSeen:      true,
		Skipped:        false,
		CheckEnabled:   true,
		CheckedAt:      "2026-11-04T10:00:00Z",
	}

	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/update"})
	mustStatus(t, status, 200, data)
	if w.update.statusCalls != 1 || w.update.checkCalls != 0 {
		t.Fatalf("GET /api/update called status %d and check %d times", w.update.statusCalls, w.update.checkCalls)
	}
	body := decodeMap(t, data)
	for key, want := range map[string]any{
		"current": "0.1.0", "latest": "0.2.0", "available": true,
		"versions_behind": float64(2),
		"release_url":     "https://github.com/LLM-4-People/mailsense/releases/tag/v0.2.0",
		"notes":           "The release notes",
		"notes_seen":      true, "skipped": false, "check_enabled": true,
		"checked_at": "2026-11-04T10:00:00Z",
	} {
		if got := body[key]; got != want {
			t.Errorf("%s = %#v, want %#v", key, got, want)
		}
	}
	// The status has no list field; every documented scalar is present and
	// none is null (notes is an empty string, never null, on a fresh state).
	for _, key := range []string{"current", "notes", "notes_seen", "skipped", "available", "versions_behind", "check_enabled"} {
		if _, ok := body[key]; !ok || body[key] == nil {
			t.Errorf("%s is missing or null: %#v", key, body[key])
		}
	}
}

// TestUpdateMutationsPersist drives the three writes through the fake and
// checks their bodies and returned status.
func TestUpdateMutationsPersist(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.update.checkStatus = update.Status{Current: "0.1.0", CheckEnabled: true}
	w.update.skipStatus = update.Status{Current: "0.1.0", CheckEnabled: true, Skipped: true}
	w.update.seenStatus = update.Status{Current: "0.1.0", CheckEnabled: true, NotesSeen: true}

	status, _, data := do(t, ts, srv, request{method: "POST", path: "/api/update/check", body: "{}"})
	mustStatus(t, status, 200, data)
	if w.update.checkCalls != 1 || decodeMap(t, data)["current"] != "0.1.0" {
		t.Fatalf("check calls = %d, response = %s", w.update.checkCalls, data)
	}

	status, _, data = do(t, ts, srv, request{method: "POST", path: "/api/update/skip", body: `{"version":" 0.2.0 "}`})
	mustStatus(t, status, 200, data)
	if w.update.skipCalls != 1 || w.update.lastVersion != "0.2.0" || decodeMap(t, data)["skipped"] != true {
		t.Fatalf("skip calls = %d, version = %q, response = %s", w.update.skipCalls, w.update.lastVersion, data)
	}

	status, _, data = do(t, ts, srv, request{method: "POST", path: "/api/update/seen", body: `{"version":"0.2.0"}`})
	mustStatus(t, status, 200, data)
	if w.update.seenCalls != 1 || w.update.lastVersion != "0.2.0" || decodeMap(t, data)["notes_seen"] != true {
		t.Fatalf("seen calls = %d, version = %q, response = %s", w.update.seenCalls, w.update.lastVersion, data)
	}

	for _, body := range []string{
		`{}`,
		`{"version":""}`,
		`{"version":"` + strings.Repeat("a", maxUpdateVersionBytes+1) + `"}`,
	} {
		status, _, data = do(t, ts, srv, request{method: "POST", path: "/api/update/skip", body: body})
		mustStatus(t, status, 400, data)
		if got := errorCode(t, data); got != "invalid_request" {
			t.Fatalf("body %q code = %q, want invalid_request", body, got)
		}
	}
	if w.update.skipCalls != 1 {
		t.Fatalf("an invalid skip reached the service (%d calls)", w.update.skipCalls)
	}
}

// TestUpdateFailedCheckIsAQuietStatus: a failed upstream check is a 200
// status with error_code/last_error, the same shape the background path
// records, never an API error.
func TestUpdateFailedCheckIsAQuietStatus(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.update.checkStatus = update.Status{
		Current: "0.1.0", CheckEnabled: true,
		ErrorCode: update.ErrorRateLimited, LastError: "slow down",
		CheckedAt: "2026-11-04T10:00:00Z",
	}
	status, _, data := do(t, ts, srv, request{method: "POST", path: "/api/update/check", body: "{}"})
	mustStatus(t, status, 200, data)
	body := decodeMap(t, data)
	if body["error_code"] != update.ErrorRateLimited || body["last_error"] != "slow down" {
		t.Fatalf("failed status = %#v", body)
	}
}

// TestUpdateRoutesIgnoreTheLearningGate: the update surface is not a data
// read. A fresh install must be able to read its status and answer the
// What's New markers before learning completes, so the gate is never
// consulted; bearer auth still protects every route.
func TestUpdateRoutesIgnoreTheLearningGate(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.learning.allowErr = lockedGate()
	for _, tc := range []request{
		{method: "GET", path: "/api/update"},
		{method: "POST", path: "/api/update/check", body: "{}"},
		{method: "POST", path: "/api/update/skip", body: `{"version":"0.2.0"}`},
		{method: "POST", path: "/api/update/seen", body: `{"version":"0.2.0"}`},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			status, _, data := do(t, ts, srv, tc)
			mustStatus(t, status, 200, data)
		})
	}
	if w.learning.calls != 0 {
		t.Fatalf("the learning gate was consulted %d times", w.learning.calls)
	}
	if w.update.statusCalls != 1 || w.update.checkCalls != 1 || w.update.skipCalls != 1 || w.update.seenCalls != 1 {
		t.Fatalf("update calls: status %d check %d skip %d seen %d",
			w.update.statusCalls, w.update.checkCalls, w.update.skipCalls, w.update.seenCalls)
	}

	// Bearer auth is the whole protection for this surface; a missing bearer
	// answers 401 even with the gate closed.
	for _, tc := range []request{
		{method: "GET", path: "/api/update"},
		{method: "POST", path: "/api/update/check", body: "{}"},
		{method: "POST", path: "/api/update/skip", body: `{"version":"0.2.0"}`},
		{method: "POST", path: "/api/update/seen", body: `{"version":"0.2.0"}`},
	} {
		t.Run("no auth "+tc.method+" "+tc.path, func(t *testing.T) {
			status, _, data := do(t, ts, srv, noAuth(tc))
			mustStatus(t, status, 401, data)
			if got := errorCode(t, data); got != "unauthorized" {
				t.Fatalf("code = %q, want unauthorized", got)
			}
		})
	}
}
