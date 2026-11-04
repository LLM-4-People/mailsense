package api

import (
	"context"
	"net/http"
	"time"
)

// BootAPI reports the start-up state the desktop shell paints before its
// first route has a real answer. It reads no mail and no learning status.
type BootAPI interface {
	Boot(ctx context.Context) (BootView, error)
}

// BootView is the boot strip contract. Views is always an array in the warm
// order; a failed view stays in the list with State "failed", and a view the
// resource guard refused stays with State "skipped" and its reason in Error.
// Skipped counts those views separately: they never ran, so they are neither
// done nor failed, and the route that owns each one builds it on first use.
type BootView struct {
	State     string          `json:"state"`
	StartedAt time.Time       `json:"started_at"`
	ElapsedMS int64           `json:"elapsed_ms"`
	Done      int             `json:"done"`
	Total     int             `json:"total"`
	Failed    int             `json:"failed"`
	Skipped   int             `json:"skipped"`
	Current   string          `json:"current"`
	Views     []BootStageView `json:"views"`
}

// BootStageView is one warm stage. DurationMS is zero until it finishes.
// Error names why the stage did not build: the failure text, or the resource
// ceiling that made the warm skip it. A skipped stage has no duration because
// it never ran.
type BootStageView struct {
	Name       string `json:"name"`
	State      string `json:"state"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

func (s *Server) handleBoot(w http.ResponseWriter, r *http.Request) {
	view, err := s.services.Boot.Boot(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	view.Views = nonNilSlice(view.Views)
	s.writeJSON(w, r, http.StatusOK, view)
}
