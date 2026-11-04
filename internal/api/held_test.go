package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/store"
)

// TestHeldRemovalsListAndConfirm: the list names the source beside the
// folder, and confirming reaches the one delete owner with the folder id.
func TestHeldRemovalsListAndConfirm(t *testing.T) {
	srv, ts, w := newTestServer(t)
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/held-removals?source=outlook"})
	mustStatus(t, status, http.StatusOK, data)
	var view HeldRemovalsView
	if err := json.Unmarshal(data, &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Folders) != 1 || view.Folders[0].SourceID != "outlook" ||
		view.Folders[0].FolderID != 7 || view.Folders[0].Held != 3 || view.Folders[0].Stored != 100 {
		t.Fatalf("folders = %+v", view.Folders)
	}
	// The auto-apply deadline is part of the wire: the screen renders it
	// instead of promising the messages stay forever.
	if got := view.Folders[0].AppliesAt; got != "2026-11-06T00:00:00Z" {
		t.Fatalf("applies_at = %q", got)
	}
	if w.held.source != "outlook" {
		t.Fatalf("source = %q", w.held.source)
	}

	status, _, data = do(t, ts, srv, request{method: "POST", path: "/api/held-removals/7/confirm"})
	mustStatus(t, status, http.StatusOK, data)
	if w.held.confirmed != 7 {
		t.Fatalf("confirmed = %d, want 7", w.held.confirmed)
	}
	var result HeldRemovalConfirmView
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Removal.Deleted != 3 || result.Removal.Held != 0 {
		t.Fatalf("removal = %+v", result.Removal)
	}
}

// TestHeldRemovalConfirmRejectsInvalidFolder: a malformed path segment never
// reaches the delete owner.
func TestHeldRemovalConfirmRejectsInvalidFolder(t *testing.T) {
	srv, ts, w := newTestServer(t)
	status, _, data := do(t, ts, srv, request{method: "POST", path: "/api/held-removals/abc/confirm"})
	mustStatus(t, status, http.StatusBadRequest, data)
	if w.held.confirmed != 0 {
		t.Fatal("an invalid folder id reached the service")
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "invalid_id" {
		t.Fatalf("code = %q", body.Error.Code)
	}
}

// TestHeldRemovalsServiceErrorIsMapped: a store failure keeps the API
// contract instead of returning an empty success.
func TestHeldRemovalsServiceErrorIsMapped(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.held.err = store.ErrNotFound
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/held-removals"})
	mustStatus(t, status, http.StatusNotFound, data)
}
