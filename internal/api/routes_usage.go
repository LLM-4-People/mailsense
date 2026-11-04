package api

import "net/http"

// usageDefaultDays and usageMaxDays bound GET /api/usage: a month by default,
// at most a year. The window itself belongs to the store query so the route
// never reads an unbounded table.
const (
	usageDefaultDays = 30
	usageMinDays     = 1
	usageMaxDays     = 365
)

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	days, err := boundedQueryInt(r, "days", usageDefaultDays, usageMinDays, usageMaxDays)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	rows, err := s.services.Usage.List(r.Context(), days)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, UsageViewFor(rows, days))
}
