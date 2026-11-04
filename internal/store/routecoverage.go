package store

import (
	"context"
	"encoding/json"
	"fmt"
)

// QuestionKey is one profile question's name and type: the two facts the
// route-coverage aggregate compares against a stored answer. The store does
// not import the triage taxonomy; the caller passes them as data.
type QuestionKey struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// RouteCoverage is the triage summary's coverage over the messages a filter
// selects. Eligible is the population; Total counts the messages with a
// route row. Stale takes precedence over Partial, and every other covered
// message is Fresh: Total = Fresh + Stale + Partial. Failed counts the
// members whose last triage attempt failed (it may overlap the other
// counts, exactly like the legacy coverage).
type RouteCoverage struct {
	Eligible int
	Total    int
	Fresh    int
	Stale    int
	Partial  int
	Failed   int
}

// RouteCoverage counts the route answers of the messages EachMessage would
// stream for the filter, in one aggregate: the population, the messages
// with a route row, whether each row is stale, partial (a question is
// missing or has another type) or fresh, and how many members have a stored
// triage failure. Route rows live under routeKey; failures live under the
// triage key (fingerprint, decider), so a caller names both. It is the
// summary's whole read: no message is materialized and no answer is decoded
// in Go. A malformed answers payload fails the read, never silently counts
// as partial.
func (s *Store) RouteCoverage(ctx context.Context, routeKey, fingerprint, decider string, f MessageFilter, questions []QuestionKey) (RouteCoverage, error) {
	vis, err := s.visible(ctx)
	if err != nil {
		return RouteCoverage{}, err
	}
	where, args := f.sql(vis)
	byName := make(map[string]string, len(questions))
	for _, q := range questions {
		byName[q.Name] = q.Type
	}
	raw, err := json.Marshal(byName)
	if err != nil {
		return RouteCoverage{}, fmt.Errorf("store: route coverage questions: %w", err)
	}
	args = append(args, string(raw), fingerprint, decider, routeKey)
	var c RouteCoverage
	if err := s.db.QueryRowContext(ctx, routeCoverageQuery(where), args...).
		Scan(&c.Eligible, &c.Total, &c.Stale, &c.Partial, &c.Failed); err != nil {
		return RouteCoverage{}, fmt.Errorf("store: route coverage: %w", err)
	}
	c.Fresh = c.Total - c.Stale - c.Partial
	return c, nil
}

// routeCoverageQuery is the one statement of RouteCoverage over the member
// predicate. The questions are bound as one JSON object ({name: type}) and
// json_each yields each pair directly, so no question object is decoded in
// SQL. Fresh is Total minus Stale and Partial: the three cases are disjoint,
// so the aggregate evaluates the question list once per fresh or partial
// row instead of scanning every answer twice. Total counts every member
// route row through json_type, so a malformed payload fails on a stale row
// too, exactly like the Go decode did. IS NOT is deliberate: a missing
// question extracts NULL and counts as partial. Failed counts a member's
// failure row under the triage key, independently of its route row (as the
// legacy coverage did, so a failure may overlap Fresh, Partial or Stale).
// The failure lookup is the (message_id, fingerprint, decider) primary key.
// A malformed answers payload makes the JSON functions fail the statement,
// so a corrupt row never counts partial.
func routeCoverageQuery(where string) string {
	return `WITH member AS (
	SELECT m.id, m.revision
	FROM messages m JOIN folders f ON f.id = m.folder_id
	LEFT JOIN scope_verdicts v ON v.message_id = m.id
	WHERE ` + where + `
)
SELECT COUNT(*) AS eligible,
	COUNT(CASE WHEN ra.message_id IS NOT NULL AND json_type(ra.answers) IS NOT NULL THEN 1 END) AS total,
	COALESCE(SUM(CASE WHEN ra.message_id IS NOT NULL AND ra.revision <> member.revision THEN 1 ELSE 0 END), 0) AS stale,
	COALESCE(SUM(CASE WHEN ra.message_id IS NOT NULL AND ra.revision = member.revision AND EXISTS (
		SELECT 1 FROM json_each(?) q
		WHERE json_extract(ra.answers, '$.' || json_quote(q.key) || '.type') IS NOT q.value
	) THEN 1 ELSE 0 END), 0) AS partial,
	COALESCE(SUM(CASE WHEN EXISTS (
		SELECT 1 FROM triage_failures tf
		WHERE tf.message_id = member.id AND tf.fingerprint = ? AND tf.decider = ?
	) THEN 1 ELSE 0 END), 0) AS failed
FROM member LEFT JOIN route_answers ra
	ON ra.message_id = member.id AND ra.fingerprint = ?`
}
