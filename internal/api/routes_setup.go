package api

import (
	"net/http"
	"os"
)

// The setup screen's two setting keys. The schema owns their type, default and
// description; this file names the two the read consumes.
const (
	settingSetupDone  = "ui.setup.done"
	settingUILanguage = "ui.language"
)

// SetupView is the first-run state. It is a read-only projection: the wizard
// writes the marker through the ordinary settings route, and every other step
// uses the existing mailbox, zone and provider routes. Nothing here is a second
// configuration store.
type SetupView struct {
	Needed    bool   `json:"needed"`
	Done      bool   `json:"done"`
	DataDir   string `json:"data_dir"`
	TimeZone  string `json:"time_zone"`
	Language  string `json:"language"`
	Mailboxes int    `json:"mailboxes"`
	Selected  int    `json:"selected"`
	// DatabaseBytes is the size of the SQLite file on disk, so the wizard can
	// say what already exists before the first sync. DatabaseSizeKnown stays
	// false when the file cannot be read: an absent size is not an empty one.
	DatabaseBytes     int64 `json:"database_bytes"`
	DatabaseSizeKnown bool  `json:"database_size_known"`
}

// setupDatabaseBytes reads the store file size. It is a supplementary fact:
// a stat failure degrades the setup view, never fails it.
func setupDatabaseBytes(path string) (int64, bool) {
	if path == "" {
		return 0, false
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return 0, false
	}
	return info.Size(), true
}

// handleSetup answers whether the first-run screen should open. The completion
// marker is the only "finished" fact. A picked mailbox is the proof that an
// install which predates the marker already went through setup.
//
// The predicate must not count discovered mailboxes: the app runs a best-effort
// discovery at startup, so a fresh Windows install usually has store rows
// before the interface loads. Those rows are a pre-fetch for the wizard, not
// evidence that setup happened. Only a selected store closes the screen.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if s.services.Status == nil || s.services.Settings == nil || s.services.Mailboxes == nil {
		s.writeError(w, r, &APIError{
			Status:  http.StatusServiceUnavailable,
			Code:    "setup_unavailable",
			Message: "the setup state is unavailable",
		})
		return
	}
	status, err := s.services.Status.Status(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	boxes, err := s.services.Mailboxes.List(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	done := false
	if view, ok := s.services.Settings.Get(settingSetupDone); ok {
		switch value := view.Value.(type) {
		case bool:
			done = value
		case string:
			done = value == "true"
		}
	}
	language := "auto"
	if view, ok := s.services.Settings.Get(settingUILanguage); ok {
		if value, ok := view.Value.(string); ok && (value == "auto" || value == "en" || value == "fr") {
			language = value
		}
	}
	selected := 0
	for _, box := range boxes {
		if box.Selected {
			selected++
		}
	}
	databaseBytes, databaseKnown := setupDatabaseBytes(status.StorePath)
	s.writeJSON(w, r, http.StatusOK, SetupView{
		Needed:            !done && selected == 0,
		Done:              done,
		DataDir:           status.DataDir,
		TimeZone:          status.TimeZone,
		Language:          language,
		Mailboxes:         len(boxes),
		Selected:          selected,
		DatabaseBytes:     databaseBytes,
		DatabaseSizeKnown: databaseKnown,
	})
}
