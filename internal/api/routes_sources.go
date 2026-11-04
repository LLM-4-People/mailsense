package api

import "net/http"

// handleSourceAction is the one dispatcher of the owner actions a source
// exposes outside a sync. The path names the source and the action; an
// unknown action is a missing route, not an internal error. The service owns
// which connector implements which optional capability.
func (s *Server) handleSourceAction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		s.writeError(w, r, UnknownSource())
		return
	}
	if err := s.decodeOptional(r, &struct{}{}); err != nil {
		s.writeError(w, r, err)
		return
	}
	var view SourceActionView
	var err error
	switch action := r.PathValue("action"); action {
	case "start":
		view, err = s.services.Sources.Start(r.Context(), id)
	case "resume":
		view, err = s.services.Sources.Resume(r.Context(), id)
	case "retry-details":
		view, err = s.services.Sources.RetryDetails(r.Context(), id)
	default:
		s.writeError(w, r, notFoundWith("not_found", "no such source action"))
		return
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, view)
}
