package api

import (
	"net/http"

	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/layer1"
	"github.com/LLM-4-People/mailsense/internal/learning"
)

type actionResponse struct {
	Job jobs.Info `json:"job"`
}

type actionIndexRequest struct {
	Stores []string `json:"stores"`
}

// handleAction is the one action dispatcher. Each case is the learning action
// id the UI sends (run_evidence, run_layer1, run_judge) or the one name the
// screen uses directly (sync, index, backfill_html); there are no aliases, so
// a renamed action has one spelling and one owner. Jobs are started by their
// owners, so a second request while one runs is the owner's 409, not a second
// job.
func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	switch action {
	case "sync":
		var req discoverRequest
		if err := s.decodeOptional(r, &req); err != nil {
			s.writeError(w, r, err)
			return
		}
		info, err := s.services.Runs.StartSync(r.Context(), req.Source)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		s.writeJSON(w, r, http.StatusAccepted, actionResponse{Job: info})
	case learning.ActionRunEvidence:
		if err := s.decodeOptional(r, &struct{}{}); err != nil {
			s.writeError(w, r, err)
			return
		}
		info, err := s.services.Runs.StartEvidence(r.Context())
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		s.writeJSON(w, r, http.StatusAccepted, actionResponse{Job: info})
	case "index":
		var req actionIndexRequest
		if err := s.decodeOptional(r, &req); err != nil {
			s.writeError(w, r, err)
			return
		}
		info, err := s.services.Runs.StartIndex(r.Context(), req.Stores)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		s.writeJSON(w, r, http.StatusAccepted, actionResponse{Job: info})
	case learning.ActionRunLayer1:
		if err := s.decodeOptional(r, &struct{}{}); err != nil {
			s.writeError(w, r, err)
			return
		}
		info, err := s.services.Labels.Start(r.Context())
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		s.writeJSON(w, r, http.StatusAccepted, actionResponse{Job: info})
	case learning.ActionRunJudge:
		var opts layer1.JudgeOptions
		if err := s.decodeJSON(r, &opts); err != nil {
			s.writeError(w, r, err)
			return
		}
		if opts.Limit < 1 || opts.Limit > layer1.JudgeLimitMax {
			s.writeError(w, r, invalidLimit(http.StatusBadRequest, "limit", 1, layer1.JudgeLimitMax))
			return
		}
		for _, q := range opts.Questions {
			if q == "" {
				s.writeError(w, r, invalidRequest("a question is empty"))
				return
			}
		}
		info, err := s.services.Labels.StartJudge(r.Context(), opts)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		s.writeJSON(w, r, http.StatusAccepted, actionResponse{Job: info})
	case "backfill_html":
		// The one-time display-HTML re-read of stored messages. It is a
		// data-capture action like sync, not a message read: no content is
		// returned and the background job is visible/cancelable in /api/jobs.
		s.handleMessageBackfill(w, r)
	default:
		s.writeError(w, r, notFoundWith("not_found", "no such action"))
	}
}
