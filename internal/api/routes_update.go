package api

import (
	"net/http"
	"strings"
	"unicode/utf8"
)

// maxUpdateVersionBytes bounds the version a skip/seen body may carry: the
// marker is one version string, not free text.
const maxUpdateVersionBytes = 128

// updateVersionRequest is the skip and seen body: the version the marker
// applies to (the current or latest version the status reported).
type updateVersionRequest struct {
	Version string `json:"version"`
}

// handleUpdate is the local status read. It never triggers a release check:
// detection is POST /api/update/check or the background interval, so a
// screen refresh cannot spend the GitHub rate limit.
func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, r, http.StatusOK, s.services.Update.Status())
}

// handleUpdateCheck runs one release check. A failed endpoint answer is a
// quiet status (error_code/last_error set), not an API error: the UI shows
// the same state it would after a background check.
func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if err := s.decodeOptional(r, &struct{}{}); err != nil {
		s.writeError(w, r, err)
		return
	}
	status, err := s.services.Update.Check(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, status)
}

// handleUpdateSkip mutes the offer for one version until the remind window.
func (s *Server) handleUpdateSkip(w http.ResponseWriter, r *http.Request) {
	version, err := s.decodeUpdateVersion(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	status, err := s.services.Update.Skip(version)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, status)
}

// handleUpdateSeen records that the What's New notes were shown.
func (s *Server) handleUpdateSeen(w http.ResponseWriter, r *http.Request) {
	version, err := s.decodeUpdateVersion(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	status, err := s.services.Update.MarkSeen(version)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, status)
}

// decodeUpdateVersion reads the one version body of the skip and seen
// routes: required, trimmed, bounded.
func (s *Server) decodeUpdateVersion(r *http.Request) (string, error) {
	var req updateVersionRequest
	if err := s.decodeJSON(r, &req); err != nil {
		return "", err
	}
	version := strings.TrimSpace(req.Version)
	if version == "" || len(version) > maxUpdateVersionBytes || !utf8.ValidString(version) {
		return "", &APIError{Status: http.StatusBadRequest, Code: "invalid_request",
			Message: "a version is required", Params: map[string]string{"name": "version"}}
	}
	return version, nil
}
