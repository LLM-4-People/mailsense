package api

import (
	"net/http"

	"github.com/LLM-4-People/mailsense/internal/priority"
)

// handlePriority is the gated owner-only ranking page. The service owns the
// score and the cache; the handler only validates the query.
func (s *Server) handlePriority(w http.ResponseWriter, r *http.Request) {
	filter, ok := priority.ParseFilter(r.URL.Query().Get("filter"))
	if !ok {
		s.writeError(w, r, &APIError{Status: http.StatusBadRequest, Code: "invalid_filter",
			Message: "unknown filter", Params: map[string]string{"name": "filter"}})
		return
	}
	limit, err := boundedQueryInt(r, "limit", priority.DefaultLimit, 1, priority.MaxLimit)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	offset, err := boundedQueryOffset(r, "offset", 0, -1)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	page, err := s.services.Priority.RankPage(r.Context(), filter, limit, offset)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, PriorityView{Items: PriorityRows(page.Items), Total: page.Total,
		Available: page.Available, Offset: page.Offset, HasMore: page.HasMore, Counts: page.Counts,
		GeneratedAt: wireTime(page.GeneratedAt), Preview: page.Preview})
}
