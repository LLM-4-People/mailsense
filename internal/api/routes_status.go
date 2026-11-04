package api

import (
	"errors"
	"net/http"
	"os"

	"github.com/LLM-4-People/mailsense/internal/buildinfo"
	"github.com/LLM-4-People/mailsense/internal/learning"
)

type zoneRequest struct {
	Zone string `json:"zone"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	view, err := s.services.Status.Status(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	view.Version = buildinfo.Version
	view.Launch = s.launch
	view.PID = os.Getpid()
	view.StartedAt = s.startedAt
	view.URL = s.URL()
	view.SSEClients = s.hub.Count()
	if s.services.Resources != nil {
		// A failed reading is a degraded status line, never a failed status
		// request: the owner still needs the app state.
		if state, err := s.services.Resources.State(r.Context()); err != nil {
			s.log.Warn("resource status unavailable", "err", err)
		} else {
			view.Resources = &state
		}
	}
	view.StoreMeta = nonNilMap(view.StoreMeta)
	view.Sources = nonNilSlice(view.Sources)
	view.RestartPending = nonNilSlice(view.RestartPending)
	s.writeJSON(w, r, http.StatusOK, view)
}

func (s *Server) handleLearning(w http.ResponseWriter, r *http.Request) {
	view, err := s.learningView(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, view)
}

// handleGate answers the one gate question with one reading. On the
// open/latched path the reading returns after the learned-scope latch read; on
// a locked path it builds the status and the lock carries it, so the UI can
// paint the existing locked screen. Reading the rule three times (preview,
// open, allow) used to let a learning event land between them and produce a
// view that never existed.
func (s *Server) handleGate(w http.ResponseWriter, r *http.Request) {
	gate := s.services.Learning.GateState(r.Context())
	if gate.Allow == nil {
		s.writeJSON(w, r, http.StatusOK, GateView{Allowed: true, Open: gate.Open, Preview: gate.Preview})
		return
	}
	var locked *learning.LockedError
	if errors.As(gate.Allow, &locked) {
		status := locked.Status
		s.writeJSON(w, r, http.StatusOK, GateView{Open: gate.Open, Preview: gate.Preview, Status: &status})
		return
	}
	s.writeError(w, r, gate.Allow)
}

func (s *Server) handleZone(w http.ResponseWriter, r *http.Request) {
	var req zoneRequest
	if err := s.decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	if req.Zone == "" {
		s.writeError(w, r, invalidRequest("zone is required"))
		return
	}
	if err := s.services.Learning.ConfirmZone(r.Context(), req.Zone); err != nil {
		s.writeError(w, r, err)
		return
	}
	view, err := s.learningView(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, view)
}

func (s *Server) learningView(r *http.Request) (*LearningView, error) {
	status, gate, err := s.services.Learning.Status(r.Context())
	if err != nil {
		return nil, err
	}
	return &LearningView{Status: status, Open: gate.Open, Preview: gate.Preview}, nil
}
