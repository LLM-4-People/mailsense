package api

import (
	"net/http"
)

// handleHeldRemovals lists the folders whose removals the safety valve held
// for the owner's confirmation. An empty source lists every registered
// source; a source the owner names narrows it.
func (s *Server) handleHeldRemovals(w http.ResponseWriter, r *http.Request) {
	if !s.requireHeldRemovals(w, r) {
		return
	}
	folders, err := s.services.HeldRemovals.Held(r.Context(), r.URL.Query().Get("source"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	folders = nonNilSlice(folders)
	s.writeJSON(w, r, http.StatusOK, HeldRemovalsView{Folders: folders})
}

// handleHeldRemovalConfirm applies one folder's held removals through the
// syncer, the owner of the deletion and its move/delete classification.
func (s *Server) handleHeldRemovalConfirm(w http.ResponseWriter, r *http.Request) {
	if !s.requireHeldRemovals(w, r) {
		return
	}
	folderID, err := pathID(r, "folder")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	removal, err := s.services.HeldRemovals.Confirm(r.Context(), folderID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, HeldRemovalConfirmView{Removal: removal})
}

// requireHeldRemovals reports whether the held-removal service is wired. Both
// routes answer the same 503 problem when it is not.
func (s *Server) requireHeldRemovals(w http.ResponseWriter, r *http.Request) bool {
	if s.services.HeldRemovals != nil {
		return true
	}
	s.writeError(w, r, &APIError{Status: http.StatusServiceUnavailable, Code: "unavailable",
		Message: "held removals are unavailable"})
	return false
}
