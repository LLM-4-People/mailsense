package api

import (
	"errors"
	"net/http"

	"github.com/LLM-4-People/mailsense/internal/brief"
)

// handleBrief is the gated deterministic Brief. The service owns the default
// window, the section caps, evidence and every count; the handler validates
// the optional since boundary and returns the service result unchanged
// except for the contract's always-present arrays.
func (s *Server) handleBrief(w http.ResponseWriter, r *http.Request) {
	result, err := s.services.Brief.Brief(r.Context(), r.URL.Query().Get("since"))
	if err != nil {
		if errors.Is(err, brief.ErrInvalidSince) {
			s.writeError(w, r, &APIError{Status: http.StatusBadRequest, Code: "invalid_since_time",
				Message: "since must be an RFC3339 timestamp and must not be in the future",
				Params:  map[string]string{"name": "since"}})
			return
		}
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, briefView(result))
}

// briefView keeps the wire contract on an implementation boundary: every
// section exists and every items/evidence list is an array, never null.
func briefView(result brief.Result) brief.Result {
	sections := []*brief.Section{
		&result.Must, &result.Waiting, &result.Changed, &result.Relationships, &result.Topics,
	}
	for _, section := range sections {
		section.Items = nonNilSlice(section.Items)
		for i := range section.Items {
			section.Items[i].Evidence = nonNilSlice(section.Items[i].Evidence)
		}
	}
	return result
}
