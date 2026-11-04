package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// TestSetupOpensOnAFreshInstall is the first-run predicate: no marker and no
// mailbox means the screen opens once.
func TestSetupOpensOnAFreshInstall(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.status.view = StatusView{DataDir: `C:\data`, TimeZone: "America/Toronto"}
	w.mailboxes.list = nil

	code, _, body := do(t, ts, srv, request{method: "GET", path: "/api/setup"})
	if code != 200 {
		t.Fatalf("status = %d, body = %s", code, body)
	}
	var view SetupView
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	if !view.Needed || view.Done {
		t.Fatalf("view = %+v, want needed and not done", view)
	}
	if view.DataDir != `C:\data` || view.TimeZone != "America/Toronto" {
		t.Fatalf("view = %+v, want the status projection", view)
	}
	if view.Language != "auto" || view.Mailboxes != 0 {
		t.Fatalf("view = %+v, want language auto and no mailbox", view)
	}
}

// TestSetupStaysClosedAfterTheMarker proves the marker wins even when the
// mailbox list is empty, so a skipped setup does not reopen on every start.
func TestSetupStaysClosedAfterTheMarker(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.settings.views = map[string]SettingView{
		settingSetupDone: {Value: true},
	}

	code, _, body := do(t, ts, srv, request{method: "GET", path: "/api/setup"})
	if code != 200 {
		t.Fatalf("status = %d, body = %s", code, body)
	}
	var view SetupView
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	if view.Needed || !view.Done {
		t.Fatalf("view = %+v, want done and not needed", view)
	}
}

// TestSetupStaysClosedForAnExistingMailbox protects an install that predates
// the marker: a selected mailbox is proof that setup already happened.
func TestSetupStaysClosedForAnExistingMailbox(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.mailboxes.list = []store.MailboxView{{Mailbox: source.Mailbox{Key: "outlook:store-1"}, Selected: true}}

	code, _, body := do(t, ts, srv, request{method: "GET", path: "/api/setup"})
	if code != 200 {
		t.Fatalf("status = %d, body = %s", code, body)
	}
	var view SetupView
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	if view.Needed || view.Done {
		t.Fatalf("view = %+v, want neither needed nor done", view)
	}
	if view.Mailboxes != 1 {
		t.Fatalf("mailboxes = %d, want 1", view.Mailboxes)
	}
}

// TestSetupStaysOpenForDiscoveredStores is the first-run regression. The app
// discovers Outlook stores before the interface loads, so those rows exist on a
// fresh install. Discovery is a pre-fetch for the wizard, not a completed
// setup: only a selected store closes the screen.
func TestSetupStaysOpenForDiscoveredStores(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.mailboxes.list = []store.MailboxView{
		{Mailbox: source.Mailbox{Key: "outlook:store-1"}, Discovered: true},
		{Mailbox: source.Mailbox{Key: "outlook:store-2"}, Discovered: true},
	}

	code, _, body := do(t, ts, srv, request{method: "GET", path: "/api/setup"})
	if code != 200 {
		t.Fatalf("status = %d, body = %s", code, body)
	}
	var view SetupView
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	if !view.Needed {
		t.Fatalf("view = %+v, want setup needed for discovered but unselected stores", view)
	}
	if view.Mailboxes != 2 || view.Selected != 0 {
		t.Fatalf("view = %+v, want 2 discovered and 0 selected", view)
	}
}

// TestSetupProjectsTheLanguage keeps the wizard's preselect honest.
func TestSetupProjectsTheLanguage(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.settings.views = map[string]SettingView{
		settingUILanguage: {Value: "fr"},
	}

	code, _, body := do(t, ts, srv, request{method: "GET", path: "/api/setup"})
	if code != 200 {
		t.Fatalf("status = %d, body = %s", code, body)
	}
	var view SetupView
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	if view.Language != "fr" {
		t.Fatalf("language = %q, want fr", view.Language)
	}
}

// TestSetupReportsTheDatabaseSize keeps the storage fact honest before the
// first sync. The size is read once from the store file the status route
// already names.
func TestSetupReportsTheDatabaseSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mailsense.db")
	body := make([]byte, 1234)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	srv, ts, w := newTestServer(t)
	w.status.view = StatusView{DataDir: t.TempDir(), StorePath: path}

	code, _, bodyBytes := do(t, ts, srv, request{method: "GET", path: "/api/setup"})
	if code != 200 {
		t.Fatalf("status = %d, body = %s", code, bodyBytes)
	}
	var view SetupView
	if err := json.Unmarshal(bodyBytes, &view); err != nil {
		t.Fatal(err)
	}
	if !view.DatabaseSizeKnown || view.DatabaseBytes != int64(len(body)) {
		t.Fatalf("view = %+v, want the store file size %d", view, len(body))
	}
}

// TestSetupDegradesWhenTheDatabaseSizeIsUnknown protects the optional read:
// a missing store file leaves the size unknown and never fails the view.
func TestSetupDegradesWhenTheDatabaseSizeIsUnknown(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.status.view = StatusView{DataDir: t.TempDir(), StorePath: filepath.Join(t.TempDir(), "absent.db")}

	code, _, body := do(t, ts, srv, request{method: "GET", path: "/api/setup"})
	if code != 200 {
		t.Fatalf("status = %d, body = %s", code, body)
	}
	var view SetupView
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatal(err)
	}
	if view.DatabaseSizeKnown || view.DatabaseBytes != 0 {
		t.Fatalf("view = %+v, want an unknown size", view)
	}
	if !view.Needed {
		t.Fatalf("view = %+v, the unknown size must not change the predicate", view)
	}
}

// TestFinishingSetupThroughTheSettingsRouteClosesIt proves the wizard writes
// no second store: the ordinary settings route sets the marker, and the next
// setup read honors it. It also proves the read is idempotent.
func TestFinishingSetupThroughTheSettingsRouteClosesIt(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.mailboxes.list = nil
	w.settings.views = map[string]SettingView{
		settingSetupDone: {Value: false},
	}

	code, _, body := do(t, ts, srv, request{method: "GET", path: "/api/setup"})
	if code != 200 {
		t.Fatalf("first read: status = %d, body = %s", code, body)
	}
	var before SetupView
	if err := json.Unmarshal(body, &before); err != nil {
		t.Fatal(err)
	}
	if !before.Needed {
		t.Fatalf("before = %+v, want setup needed", before)
	}

	code, _, body = do(t, ts, srv, request{
		method: "PUT",
		path:   "/api/settings/" + settingSetupDone,
		body:   `{"value":true}`,
	})
	if code != 200 {
		t.Fatalf("marker write: status = %d, body = %s", code, body)
	}

	for i := 0; i < 2; i++ {
		code, _, body = do(t, ts, srv, request{method: "GET", path: "/api/setup"})
		if code != 200 {
			t.Fatalf("read %d: status = %d, body = %s", i+2, code, body)
		}
		var after SetupView
		if err := json.Unmarshal(body, &after); err != nil {
			t.Fatal(err)
		}
		if after.Needed || !after.Done {
			t.Fatalf("after read %d = %+v, want done and not needed", i+2, after)
		}
	}
}
