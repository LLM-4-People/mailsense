package api

import (
	"net/http"
)

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	includeAttrs, err := queryLogAttrs(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	since, hasSince, err := queryUint(r, "since", nil)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	limit, err := boundedQueryInt(r, "limit", defaultLogsLimit, 1, maxLogsPage)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	snapshot, err := s.services.Logs.Snapshot(LogQuery{Since: since, HasSince: hasSince, Limit: limit})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	snapshot.Entries = nonNilSlice(snapshot.Entries)
	// The attrs switch is the page's one projection: attrs=off removes the
	// maps and records which rows have one to fetch. The default page keeps
	// its previous shape, with has_attrs absent.
	for i := range snapshot.Entries {
		if includeAttrs {
			snapshot.Entries[i].HasAttrs = false
			continue
		}
		snapshot.Entries[i].HasAttrs = len(snapshot.Entries[i].Attrs) > 0
		snapshot.Entries[i].Attrs = nil
	}
	s.writeJSON(w, r, http.StatusOK, snapshot)
}

// handleLogEntry returns exactly one retained entry with its attributes. It
// is the lazy half of a Logs page: one bounded ring lookup after seq-1, and
// a 404 when that window does not start at seq (rotated out or never
// existed). It never reads or returns another entry.
func (s *Server) handleLogEntry(w http.ResponseWriter, r *http.Request) {
	seq, err := pathID(r, "seq")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	entries, _, _ := s.services.Logs.ReadLimit(uint64(seq-1), 1, false)
	if len(entries) != 1 || entries[0].Seq != uint64(seq) {
		s.writeError(w, r, logEntryNotFound())
		return
	}
	s.writeJSON(w, r, http.StatusOK, entries[0])
}

// queryLogAttrs reads the Logs page's attribute switch. on is the default,
// so an existing client keeps the full entry; off leaves attrs to the
// one-entry read of an expanded row.
func queryLogAttrs(r *http.Request) (bool, error) {
	switch value := r.URL.Query().Get("attrs"); value {
	case "", "on":
		return true, nil
	case "off":
		return false, nil
	default:
		return false, &APIError{Status: http.StatusBadRequest, Code: "invalid_attrs",
			Message: "attrs must be on or off", Params: map[string]string{"name": "attrs"}}
	}
}

func logEntryNotFound() *APIError { return notFoundWith("log_not_found", "no such log entry") }
