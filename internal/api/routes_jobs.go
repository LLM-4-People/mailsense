package api

import (
	"net/http"

	"github.com/LLM-4-People/mailsense/internal/jobs"
)

// jobNotFound is the one answer of the job detail and the job cancel route.
func jobNotFound() *APIError { return notFoundWith("not_found", "no such job") }

type jobsView struct {
	Jobs []jobs.Info `json:"jobs"`
}

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	list := s.services.Jobs.List()
	list = nonNilSlice(list)
	s.writeJSON(w, r, http.StatusOK, jobsView{Jobs: list})
}

func (s *Server) handleJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	info, ok := s.services.Jobs.Get(id)
	if !ok {
		s.writeError(w, r, jobNotFound())
		return
	}
	s.writeJSON(w, r, http.StatusOK, info)
}

func (s *Server) handleJobCancel(w http.ResponseWriter, r *http.Request) {
	if err := s.decodeOptional(r, &struct{}{}); err != nil {
		s.writeError(w, r, err)
		return
	}
	info, ok := s.services.Jobs.Cancel(r.PathValue("id"))
	if !ok {
		s.writeError(w, r, jobNotFound())
		return
	}
	s.writeJSON(w, r, http.StatusAccepted, info)
}
