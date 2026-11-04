package api

import (
	"net/http"
	"strconv"
	"strings"

	worldsvc "github.com/LLM-4-People/mailsense/internal/world"
)

// handleCompanies is the gated owner-only external-organization ranking. The
// world service owns the ranking and the coverage; the handler validates the
// page size only.
func (s *Server) handleCompanies(w http.ResponseWriter, r *http.Request) {
	limit, err := boundedQueryInt(r, "limit", worldsvc.DefaultCompanyLimit,
		worldsvc.MinCompanyLimit, worldsvc.MaxCompanyLimit)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(search) > worldsvc.MaxCompanySearchLen {
		s.writeError(w, r, &APIError{Status: http.StatusBadRequest, Code: "invalid_query",
			Message: "q must be at most " + strconv.Itoa(worldsvc.MaxCompanySearchLen) + " bytes",
			Params:  map[string]string{"name": "q", "max": strconv.Itoa(worldsvc.MaxCompanySearchLen)}})
		return
	}
	result, err := s.services.Companies.List(r.Context(), worldsvc.ListQuery{Limit: limit, Search: search})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, CompaniesViewOf(result))
}

// handleCompany is the gated owner-only deep profile of one organization.
// The key is the router-decoded path segment; the world service remains the
// companyWindow reads the two window parameters every profile route shares.
// One owner keeps the ranges, defaults and error shape identical between the
// single-profile and batch-profile reads.
func companyWindow(r *http.Request) (months, eventDays int, err error) {
	months, err = boundedQueryInt(r, "months", worldsvc.DefaultCompanyMonths,
		worldsvc.MinCompanyMonths, worldsvc.MaxCompanyMonths)
	if err != nil {
		return 0, 0, err
	}
	eventDays, err = boundedQueryInt(r, "event_days", worldsvc.DefaultCompanyEventDays,
		worldsvc.MinCompanyEventDays, worldsvc.MaxCompanyEventDays)
	if err != nil {
		return 0, 0, err
	}
	return months, eventDays, nil
}

// owner of whether the key resolves.
func (s *Server) handleCompany(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !worldsvc.ValidCompanyKey(key) {
		s.writeError(w, r, invalidCompany())
		return
	}
	months, eventDays, err := companyWindow(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	view, err := s.services.Companies.Profile(r.Context(), key, months, eventDays)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, CompanyProfileViewOf(view))
}

// handleCompanyProfiles is the Companies explorer's visible-row prefetch:
// one batch read for every profile currently in view, one store scan. The
// batch is bounded by world.MaxProfileBatch, the profile cache's own size.
// The single-profile route stays the owner of a click that missed the
// prefetch.
func (s *Server) handleCompanyProfiles(w http.ResponseWriter, r *http.Request) {
	// A company key can contain a comma ("Acme, Inc."), so the batch takes
	// repeated key parameters instead of one comma-separated string.
	values := r.URL.Query()["key"]
	if len(values) == 0 {
		s.writeError(w, r, &APIError{Status: http.StatusBadRequest, Code: "invalid_keys",
			Message: "key is required"})
		return
	}
	keys := make([]string, 0, worldsvc.MaxProfileBatch)
	seen := map[string]bool{}
	for _, value := range values {
		key := strings.TrimSpace(value)
		if key == "" {
			continue
		}
		if !worldsvc.ValidCompanyKey(key) {
			s.writeError(w, r, invalidCompany())
			return
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		keys = append(keys, key)
	}
	if len(keys) == 0 || len(keys) > worldsvc.MaxProfileBatch {
		s.writeError(w, r, &APIError{Status: http.StatusBadRequest, Code: "invalid_keys",
			Message: "key must name between 1 and " + strconv.Itoa(worldsvc.MaxProfileBatch) + " companies",
			Params:  map[string]string{"max": strconv.Itoa(worldsvc.MaxProfileBatch)}})
		return
	}
	months, eventDays, err := companyWindow(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	views, err := s.services.Companies.Profiles(r.Context(), keys, months, eventDays)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, struct {
		GeneratedAt string                        `json:"generated_at"`
		Profiles    map[string]CompanyProfileWire `json:"profiles"`
	}{
		GeneratedAt: wireTime(s.clock.Now()),
		Profiles:    CompanyProfileViewsOf(views),
	})
}

func invalidCompany() *APIError {
	return &APIError{Status: http.StatusBadRequest, Code: "invalid_company", Message: "invalid company key"}
}
