package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/route"
)

// RouteAnswer is one message's route answers under a profile: the JSON
// map question → route.Answer (one entry per question, an abstention
// included) and the run and message revision it was computed at.
// DecidedAt is stamped by the store when a commit writes the row.
type RouteAnswer struct {
	MessageID int64
	Revision  int64
	RunID     int64
	Answers   string
	DecidedAt string
}

// RouteRef is one stored route answer's message id and the route revision
// it was computed at, without its answers payload: the triage list turns a
// page's ids into message rows and marks staleness against the message
// revision.
type RouteRef struct {
	MessageID int64
	Revision  int64
}

// routePageIndex is the total's access path. The (fingerprint, message_id)
// index enumerates exactly the fingerprint's route rows; the count has no
// order, so the smaller, bounded population is the right outer loop.
const routePageIndex = "route_answers_by_fingerprint"

// routePageFingerprint is the one route predicate of the page and its total:
// the stored answers under the requested profile.
const routePageFingerprint = "ra.fingerprint = ?"

// routePageJoins is the one join tail of the page and its total: the main
// folder and the owner verdict.
const routePageJoins = `
		JOIN folders f ON f.id = m.folder_id LEFT JOIN scope_verdicts v ON v.message_id = m.id`

// routePageWhere is the one membership predicate of the page and its total.
// where is MessageFilter.sql's predicate (visibility, triage roles, owner
// scope, row-id filters), so count and page cannot disagree on membership.
func routePageWhere(where string) string {
	return routePageFingerprint + " AND " + where
}

// routePageTotalFrom is the total's join. INDEXED BY pins the count to the
// route index regardless of the statistics.
func routePageTotalFrom(where string) string {
	return ` FROM route_answers ra INDEXED BY ` + routePageIndex + `
		JOIN messages m ON m.id = ra.message_id` + routePageJoins + `
		WHERE ` + routePageWhere(where)
}

// routePageOrderedFrom is the page's join: it streams messages in display
// order from the instant index, then probes route_answers by its primary key
// (message_id, fingerprint). CROSS JOIN fixes that outer loop: joining from
// route_answers and sorting by instant would build a temporary B-tree over
// the whole filtered population, and the route index only offers message-id
// order (an old backfilled message can carry a larger id than a newer one).
func routePageOrderedFrom(where string) string {
	return ` FROM messages m INDEXED BY ` + messageListOrderIndex + `
		CROSS JOIN route_answers ra ON ra.message_id = m.id` + routePageJoins + `
		WHERE ` + routePageWhere(where)
}

// routePageQuery is the page statement: newest message first, by the stored
// mail instant and then row id. The total uses routePageTotalQuery over the
// same predicate, so count and page cannot disagree on membership.
func routePageQuery(where string) string {
	return "SELECT ra.message_id, ra.revision" + routePageOrderedFrom(where) +
		" ORDER BY " + instantSQL + " DESC, m.id DESC LIMIT ? OFFSET ?"
}

func routePageTotalQuery(where string) string {
	return "SELECT COUNT(*)" + routePageTotalFrom(where)
}

// routePageTotalKey is one cached total: the same mail and route
// generations, fingerprint and filter. A route commit changes route rows
// without touching the mail, so RouteGeneration is part of the key; a mail,
// folder, scope or selection write changes the population through
// MailGeneration.
type routePageTotalKey struct {
	generation      uint64
	routeGeneration uint64
	fingerprint     string
	filter          string
}

// routePageFilterKey identifies the exact filter predicate: the SQL text plus
// every bound argument, so two role or row-id filters with the same shape
// cannot share a total.
func routePageFilterKey(where string, args []any) string {
	var b strings.Builder
	b.WriteString(where)
	for _, arg := range args {
		fmt.Fprintf(&b, "\x00%T:%v", arg, arg)
	}
	return b.String()
}

// RoutePage is one page of a fingerprint's route rows over the message
// filter, newest message first, with the total row count: the one query
// the triage list needs (no message rows, no answers). The total is cached
// per mail/route generation, fingerprint and filter: every page of one open
// asks for the same total, and recounting scans every route row of the
// fingerprint although neither the mail nor the route changed.
func (s *Store) RoutePage(ctx context.Context, fingerprint string, f MessageFilter, limit, offset int) ([]RouteRef, int, error) {
	vis, err := s.visible(ctx)
	if err != nil {
		return nil, 0, err
	}
	where, args := f.sql(vis)
	key := routePageTotalKey{
		generation:      s.MailGeneration(),
		routeGeneration: s.RouteGeneration(),
		fingerprint:     fingerprint,
		filter:          routePageFilterKey(where, args),
	}
	total, err := s.routePageTotal(ctx, key, where, args)
	if err != nil {
		return nil, 0, err
	}
	refs, err := query(ctx, s.db, routePageQuery(where),
		append(append([]any{fingerprint}, args...), limit, offset),
		func(r *sql.Rows, ref *RouteRef) error { return r.Scan(&ref.MessageID, &ref.Revision) })
	return refs, total, err
}

// routePageTotal returns the cached total of one page population, computing it
// once per key. A cold cache computes the count through the same from and
// predicate as the page, so the returned total is the page's membership
// count.
func (s *Store) routePageTotal(ctx context.Context, key routePageTotalKey, where string, args []any) (int, error) {
	s.routeTotalsMu.Lock()
	total, ok := s.routeTotals[key]
	s.routeTotalsMu.Unlock()
	if ok {
		return total, nil
	}
	if err := s.db.QueryRowContext(ctx, routePageTotalQuery(where), append([]any{key.fingerprint}, args...)...).Scan(&total); err != nil {
		return 0, err
	}
	s.routeTotalsMu.Lock()
	if s.routeTotals == nil || len(s.routeTotals) >= 8 {
		s.routeTotals = make(map[routePageTotalKey]int, 8)
	}
	s.routeTotals[key] = total
	s.routeTotalsMu.Unlock()
	return total, nil
}

// EachRouteAnswer streams a fingerprint's route answers in message order
// without holding them all in memory; fn returning an error stops. fn must
// not use the store.
func (s *Store) EachRouteAnswer(ctx context.Context, fingerprint string, fn func(RouteAnswer) error) error {
	rows, err := s.db.QueryContext(ctx, `SELECT message_id, revision, run_id, answers, decided_at FROM route_answers
		WHERE fingerprint = ? ORDER BY message_id`, fingerprint)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var a RouteAnswer
		if err := rows.Scan(&a.MessageID, &a.Revision, &a.RunID, &a.Answers, &a.DecidedAt); err != nil {
			return err
		}
		if err := fn(a); err != nil {
			return err
		}
	}
	return rows.Err()
}

// RouteRecord is one message's decoded route answers with the row's
// metadata: the revision they were computed at (a reader marks them stale
// when the message moved on), the run that wrote them and the time.
type RouteRecord = answerRecord[map[string]route.Answer]

// LoadRouteRecords returns the route answers of the given messages with
// their revision and time. A message with no row is absent (never
// zero-filled), and an unreadable row fails the whole read: a route
// answer is not silently an abstention.
func (s *Store) LoadRouteRecords(ctx context.Context, fingerprint string, ids []int64) (map[int64]RouteRecord, error) {
	return loadAnswerRecords[map[string]route.Answer](s, ctx, routeAnswerTable, fingerprint, ids)
}

// LoadRouteAnswers returns the route answers of the given messages under a
// fingerprint (the answers only: LoadRouteRecords carries the revision
// and time a reader needs to mark them stale).
func (s *Store) LoadRouteAnswers(ctx context.Context, fingerprint string, ids []int64) (map[int64]map[string]route.Answer, error) {
	records, err := s.LoadRouteRecords(ctx, fingerprint, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]map[string]route.Answer, len(records))
	for id, r := range records {
		out[id] = r.Answers
	}
	return out, nil
}

// streamRouteAnswers writes the route answers each yields, inside tx.
func (s *Store) streamRouteAnswers(ctx context.Context, tx *sql.Tx, fingerprint string, each func(fn func(RouteAnswer) error) error) error {
	return streamAnswerTable(s, ctx, tx, routeAnswerTable, fingerprint, each)
}

// PruneRouteAnswers removes every fingerprint's route answers but keep's:
// the rows of an old profile are unreachable (they are read by the current
// fingerprint only). It returns how many rows it removed.
func (s *Store) PruneRouteAnswers(ctx context.Context, keep string) (int64, error) {
	return pruneAnswerTable(ctx, s.db, routeAnswerTable, keep)
}

// execer is the part of *sql.DB and *sql.Tx a prune needs.
type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// pruneRouteAnswers is PruneRouteAnswers inside the caller's transaction.
// The shared answer-table owner holds the empty-key guard and the one DELETE.
func pruneRouteAnswers(ctx context.Context, ex execer, keep string) (int64, error) {
	return pruneAnswerTable(ctx, ex, routeAnswerTable, keep)
}
