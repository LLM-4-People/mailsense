package api

import (
	"net/http"
	"slices"

	"github.com/LLM-4-People/mailsense/internal/commitments"
)

// handleCommitments is the gated owner-only deterministic open-loop read
// model. The service owns evidence, dedupe, ordering and facet counts; the
// handler validates the closed-set filters and the page size, then returns
// the service result unchanged.
func (s *Server) handleCommitments(w http.ResponseWriter, r *http.Request) {
	limit, err := boundedQueryInt(r, "limit", commitments.DefaultLimit, 1, commitments.MaxLimit)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	filter := commitments.Filter{Limit: limit}
	query := r.URL.Query()
	if v := query.Get("kind"); v != "" {
		if !slices.Contains(commitments.Kinds, commitments.Kind(v)) {
			s.writeError(w, r, commitments.ErrInvalidKind)
			return
		}
		filter.Kind = commitments.Kind(v)
	}
	if v := query.Get("direction"); v != "" {
		if !slices.Contains(commitments.Directions, commitments.Direction(v)) {
			s.writeError(w, r, commitments.ErrInvalidDirection)
			return
		}
		filter.Direction = commitments.Direction(v)
	}
	if v := query.Get("status"); v != "" {
		if !slices.Contains(commitments.Statuses, commitments.Status(v)) {
			s.writeError(w, r, commitments.ErrInvalidStatus)
			return
		}
		filter.Status = commitments.Status(v)
	}
	result, err := s.services.Commitments.List(r.Context(), filter)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	// The service always fills both, but a fake or a future implementation
	// must not turn the contract's array and object into null.
	result.Items = nonNilSlice(result.Items)
	result.Counts = nonNilMap(result.Counts)
	s.writeJSON(w, r, http.StatusOK, result)
}
