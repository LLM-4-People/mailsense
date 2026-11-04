package api

import (
	"net/http"
)

type sidecarRequest struct {
	Runtime string `json:"runtime"`
	Model   string `json:"model"`
	Variant string `json:"variant"`
}

// decodeSidecarRequest is the one decode and validation of the install/verify
// triple: both routes take the same fields, so a missing one must answer
// identically instead of only on one of them.
func (s *Server) decodeSidecarRequest(r *http.Request) (sidecarRequest, error) {
	var req sidecarRequest
	if err := s.decodeJSON(r, &req); err != nil {
		return req, err
	}
	if req.Runtime == "" || req.Model == "" || req.Variant == "" {
		return req, invalidRequest("runtime, model and variant are required")
	}
	return req, nil
}

type okView struct {
	OK bool `json:"ok"`
}

func (s *Server) handleSidecar(w http.ResponseWriter, r *http.Request) {
	status, err := s.services.Sidecar.Status()
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	status.Builds = nonNilSlice(status.Builds)
	status.Runtimes = nonNilSlice(status.Runtimes)
	s.writeJSON(w, r, http.StatusOK, status)
}

func (s *Server) handleSidecarInstall(w http.ResponseWriter, r *http.Request) {
	req, err := s.decodeSidecarRequest(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	info, err := s.services.Sidecar.Install(r.Context(), req.Runtime, req.Model, req.Variant)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusAccepted, actionResponse{Job: info})
}

func (s *Server) handleSidecarVerify(w http.ResponseWriter, r *http.Request) {
	req, err := s.decodeSidecarRequest(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.services.Sidecar.Verify(r.Context(), req.Runtime, req.Model, req.Variant); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, okView{OK: true})
}
