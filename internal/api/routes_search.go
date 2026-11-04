package api

import (
	"net/http"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/search"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// MaxSearchAPILimit caps one search page. The service may accept more (up
// to search.max_hits for internal callers); the HTTP surface keeps pages
// small enough for the omnibox.
const MaxSearchAPILimit = 50

// handleSearch is the gated hybrid search. The request maps to the one
// store.SearchFilter both rankings share; q-only requests keep the old
// all-scope, no-filter behaviour.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q, err := searchQuery(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	result, err := s.services.Search.Search(r.Context(), q)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, result)
}

func searchQuery(r *http.Request) (search.Query, error) {
	var out search.Query
	query := r.URL.Query()
	text := query.Get("q")
	if strings.TrimSpace(text) == "" {
		return out, &APIError{Status: http.StatusBadRequest, Code: "empty_query", Message: "the query is empty"}
	}
	out.Text = text

	ownerOnly := false
	switch scope := query.Get("scope"); scope {
	case "", "all":
	case "owner":
		ownerOnly = true
	default:
		return out, &APIError{Status: http.StatusBadRequest, Code: "invalid_scope",
			Message: "scope must be owner or all", Params: map[string]string{"name": "scope"}}
	}

	var filter store.SearchFilter
	filter.OwnerOnly = ownerOnly
	if key, present, err := queryStore(r); err != nil {
		return out, err
	} else if present {
		filter.Stores = []string{key}
	}
	if role, present, err := queryRole(r, RoleGrammar{}); err != nil {
		return out, err
	} else if present {
		filter.Roles = []mail.FolderRole{role}
	}
	if sender, present, err := querySender(r); err != nil {
		return out, err
	} else if present {
		filter.Sender = sender
	}
	switch attach := query.Get("attach"); attach {
	case "", "any":
	case "yes":
		filter.HasAttachments = new(bool)
		*filter.HasAttachments = true
	case "no":
		filter.HasAttachments = new(bool)
	default:
		return out, &APIError{Status: http.StatusBadRequest, Code: "invalid_attach",
			Message: "attach must be any, yes or no", Params: map[string]string{"name": "attach"}}
	}

	limit, err := boundedQueryInt(r, "limit", 0, 1, MaxSearchAPILimit)
	if err != nil {
		return out, err
	}
	out.Limit = limit
	// semantic=off is the omnibox's suggestion mode: the same full-text
	// ranking without a model call. The explicit Search page keeps the
	// default (on) so its result is hybrid.
	switch semantic := query.Get("semantic"); semantic {
	case "", "on":
	case "off":
		out.TextOnly = true
	default:
		return out, &APIError{Status: http.StatusBadRequest, Code: "invalid_semantic",
			Message: "semantic must be on or off", Params: map[string]string{"name": "semantic"}}
	}
	offset, err := boundedQueryOffset(r, "offset", 0, search.MaxOffset)
	if err != nil {
		return out, err
	}
	out.Offset = offset
	out.Filter = filter
	return out, nil
}
