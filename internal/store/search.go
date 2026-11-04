package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// SearchFilter narrows a search; the zero value is all visible mail
// (visibleSQL: active folders of picked stores). It is the one filter of both rankings (full text in SQL, the
// vector scan through FilterIDs), so both see the same messages.
type SearchFilter struct {
	Since time.Time         `json:"since,omitzero"` // instant ≥ Since (mail.Instant)
	Until time.Time         `json:"until,omitzero"` // instant < Until
	Roles []mail.FolderRole `json:"roles,omitempty"`
	// Stores are store keys (source.StoreKey: "store-…").
	Stores []string `json:"stores,omitempty"`
	// Sender is an address, or a domain ("@acme.com" or "acme.com").
	Sender string `json:"sender,omitempty"`
	// OwnerOnly keeps owner-facing content only: the owner verdict and a
	// store kind that is not shared, delegate or public. The default false
	// keeps shared mail searchable through /api/search.
	OwnerOnly      bool  `json:"owner_only,omitempty"`
	HasAttachments *bool `json:"has_attachments,omitempty"`
}

// IsZero reports whether f selects every message.
func (f *SearchFilter) IsZero() bool {
	return f.Since.IsZero() && f.Until.IsZero() && len(f.Roles) == 0 && len(f.Stores) == 0 && f.Sender == "" &&
		!f.OwnerOnly && f.HasAttachments == nil
}

// MessageFields reports whether the filter selects on the message row (or
// its further folders) rather than only on the folder row and the scope
// verdict. A message-owned filter cannot reuse a cached id set across a mail
// write: a re-sync rewrites a message in place (a corrected sender, instant
// or attachment flag) and a further-folder membership can move without the
// message being removed and re-inserted, so the set is rebuilt. The two
// interactive filters (all visible mail, owner scope) read folders and scope
// verdicts only; that is what lets their sets grow with new mail instead of
// being listed again.
// This classification is the one owner of that fact: every field that makes
// where() read `m.` must be true here (TestMessageFieldsMatchesTheFilterSQL
// pins it against the rendered predicate).
func (f SearchFilter) MessageFields() bool {
	return !f.Since.IsZero() || !f.Until.IsZero() || len(f.Roles) > 0 || f.Sender != "" || f.HasAttachments != nil
}

// where renders the filter over messages m joined with folders f.
func (s *Store) where(ctx context.Context, f *SearchFilter) (string, []any, error) {
	vis, err := s.visible(ctx)
	if err != nil {
		return "", nil, err
	}
	parts, args := []string{vis}, []any{}
	add := func(sql string, a ...any) { parts, args = append(parts, sql), append(args, a...) }
	since, until := "", ""
	if !f.Since.IsZero() {
		since = f.Since.UTC().Format(mail.TimeLayout)
	}
	if !f.Until.IsZero() {
		until = f.Until.UTC().Format(mail.TimeLayout)
	}
	parts, args = instantRange(parts, args, since, until, false)
	if len(f.Roles) > 0 {
		cond, a := rolesSQL(f.Roles)
		add(cond, a...)
	}
	if len(f.Stores) > 0 {
		add("f.store_key IN "+stringSetSQL, jsonList(f.Stores))
	}
	if cond, a := senderWhere(f.Sender); cond != "" {
		add(cond, a...)
	}
	if f.OwnerOnly {
		// Owner-facing content, not the per-store owner verdict: a picked
		// shared/delegate store marks its own mailbox owner as verdict=owner,
		// but its subjects, snippets and participants must not surface here.
		// The base parts[0] is the visible predicate, so widening it in place
		// keeps the one boundary without adding a second condition.
		parts[0] = ownerFacingWhere(vis)
	}
	if f.HasAttachments != nil {
		add("m.has_attachments = ?", boolInt(*f.HasAttachments))
	}
	return strings.Join(parts, " AND "), args, nil
}

// FilterIDs lists the ids of every message the filter selects. An empty
// result is an empty slice, never nil: the semantic scan reads nil as "no
// filter", so returning nil for a filter that matches nothing would leak
// every vector into a filtered search (owner scope included).
func (s *Store) FilterIDs(ctx context.Context, f SearchFilter) ([]int64, error) {
	where, args, err := s.where(ctx, &f)
	if err != nil {
		return nil, err
	}
	ids, err := query(ctx, s.db, `SELECT m.id FROM messages m JOIN folders f ON f.id = m.folder_id
		LEFT JOIN scope_verdicts v ON v.message_id = m.id WHERE `+where, args,
		func(r *sql.Rows, v *int64) error { return r.Scan(v) })
	if ids == nil {
		ids = []int64{}
	}
	return ids, err
}

// FilterIDsSince is FilterIDs restricted to the messages above after: the
// new mail a cached filter set appends instead of listing the whole mailbox
// again after every sync. It returns the ids in row-id order and the store's
// highest message id, so the caller's watermark advances even when no new
// message matches the filter. An empty result is an empty slice, never nil
// (FilterIDs documents why).
func (s *Store) FilterIDsSince(ctx context.Context, f SearchFilter, after int64) (ids []int64, maxID int64, err error) {
	where, args, err := s.where(ctx, &f)
	if err != nil {
		return nil, 0, err
	}
	if err := s.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(id), 0) FROM messages").Scan(&maxID); err != nil {
		return nil, 0, err
	}
	ids, err = query(ctx, s.db, `SELECT m.id FROM messages m JOIN folders f ON f.id = m.folder_id
		LEFT JOIN scope_verdicts v ON v.message_id = m.id WHERE `+where+` AND m.id > ? ORDER BY m.id`, append(args, after),
		func(r *sql.Rows, v *int64) error { return r.Scan(v) })
	if ids == nil {
		ids = []int64{}
	}
	return ids, maxID, err
}

// FilterSelects reports which of ids the filter selects now, by message id
// (a missing entry means "not selected"). It is the exact membership test of
// a cached filter set: the semantic ranking may run over a set that grew
// only by new mail, so a candidate that moved out of the filter (a further
// folder changed, a scope verdict was rewritten, a message was removed) is
// dropped before the rankings are fused. The candidate list drives the read
// (CROSS JOIN pins it first: without it the planner drives an owner-scoped
// filter from scope_verdicts_verdict and reads every owner verdict instead
// of the page).
func (s *Store) FilterSelects(ctx context.Context, f SearchFilter, ids []int64) (map[int64]bool, error) {
	out := make(map[int64]bool, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	where, args, err := s.where(ctx, &f)
	if err != nil {
		return nil, err
	}
	rows, err := query(ctx, s.db, `SELECT m.id FROM json_each(?) j CROSS JOIN messages m ON m.id = j.value
		JOIN folders f ON f.id = m.folder_id LEFT JOIN scope_verdicts v ON v.message_id = m.id WHERE `+where,
		append([]any{idSet(ids)}, args...), func(r *sql.Rows, v *int64) error { return r.Scan(v) })
	if err != nil {
		return nil, err
	}
	for _, id := range rows {
		out[id] = true
	}
	return out, nil
}

// FilterStamp is the cheap change detector of a cached filter set: the
// folder rows its predicate reads, the scope verdicts of the owner boundary,
// and the highest message id. The message rows themselves are covered by
// MailGeneration; this stamp covers everything a mail write does not move.
// Folders is one stable line per folder (id, active, picked, role, store
// kind, store key), so a sync that only refreshes a folder's timestamp and
// item count leaves it unchanged.
type FilterStamp struct {
	Folders  string
	Verdicts int    // scope_verdicts rows
	ScopedAt string // latest scope_verdicts.computed_at (store time, second resolution)
	MaxID    int64  // highest messages.id: a decrease means the top row was removed (its id can be reused)
	Now      string // store time when the stamp was read
}

// FilterStamp reads the filter cache's change stamp: the folder rows (one
// narrow read), the highest message id (an index lookup) and, for an
// owner-scoped filter, the scope verdicts. It never lists message ids.
// ownerScoped also reads the scope verdicts: only a filter whose predicate
// reads them can be invalidated by a verdict write, and the scan of
// scope_verdicts is the expensive half of the stamp.
//
// ScopedAt has second resolution (mail.TimeLayout). A verdict write after
// the stamp compares >= Now, so a cache built at Now must treat any stamp
// with ScopedAt >= its build time as changed: a same-second write is never
// missed, it only costs one extra rebuild.
func (s *Store) FilterStamp(ctx context.Context, ownerScoped bool) (FilterStamp, error) {
	var st FilterStamp
	st.Now = s.now()
	rows, err := s.db.QueryContext(ctx, "SELECT id, active, picked, role, store_kind, store_key FROM folders ORDER BY id")
	if err != nil {
		return st, err
	}
	var b strings.Builder
	for rows.Next() {
		var id int64
		var active, picked int
		var role, kind, key string
		if err := rows.Scan(&id, &active, &picked, &role, &kind, &key); err != nil {
			_ = rows.Close()
			return st, err
		}
		fmt.Fprintf(&b, "%d:%d:%d:%s:%s:%s;", id, active, picked, role, kind, key)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return st, err
	}
	if err := rows.Close(); err != nil {
		return st, err
	}
	st.Folders = b.String()
	if ownerScoped {
		if err := s.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM scope_verdicts),
			COALESCE((SELECT MAX(computed_at) FROM scope_verdicts), '')`).Scan(&st.Verdicts, &st.ScopedAt); err != nil {
			return st, err
		}
	}
	if err := s.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(id), 0) FROM messages").Scan(&st.MaxID); err != nil {
		return st, err
	}
	return st, nil
}

// SearchText runs a BM25-ranked full-text search within the filter, on
// the folded form (mail.Fold: case, diacritics and compatibility forms
// ignored in every script). A query with a term in a substring-searched
// script runs on the substring index (planQuery). limit < 1 or above
// search.max_hits means search.max_hits; Total counts the matches up to
// search.count_cap.
func (s *Store) SearchText(ctx context.Context, text string, f SearchFilter, limit int) (SearchResult, error) {
	return s.searchText(ctx, text, f, limit, false)
}

// SearchTextAny is the recall form of SearchText: MATCH terms are ORed, and
// an all-scan plan ORs its scans, so a natural-language question still finds
// a message when some of its words are absent. A mixed MATCH+scan plan keeps
// the scan term ANDed (FTS5 cannot OR MATCH). It is a fallback for a
// zero-result strict search, never the default ranking.
func (s *Store) SearchTextAny(ctx context.Context, text string, f SearchFilter, limit int) (SearchResult, error) {
	return s.searchText(ctx, text, f, limit, true)
}

func (s *Store) searchText(ctx context.Context, text string, f SearchFilter, limit int, anyTerm bool) (SearchResult, error) {
	return s.searchTextOn(ctx, s.db, text, f, limit, anyTerm)
}

// searchTextOn is searchText over one queryer: production passes the pool,
// a test passes a counting wrapper to pin how many statements a search
// issues.
//
// The hits are the primary scan. When it returns fewer rows than the page
// asked for, it is the whole match: the total is exact from that one scan
// and no count runs. Only a full page is ambiguous, and then the total is
// the capped count (bounded at search.count_cap+1 rows, never a full match
// scan), as before. A single window over capped rows was measured slower on
// a common word: it makes SQLite keep count_cap ranked rows where the page
// keeps limit, so the count stays a separate, capped read.
func (s *Store) searchTextOn(ctx context.Context, qr queryer, text string, f SearchFilter, limit int, anyTerm bool) (SearchResult, error) {
	var res SearchResult
	p, err := planQuery(text, anyTerm)
	if err != nil {
		return res, err
	}
	res.Terms = p.terms
	snap := s.cfg.Snapshot()
	if maxHits := snap.Int(SettingSearchMaxHits); limit < 1 || limit > maxHits {
		limit = maxHits
	}
	where, args, err := s.where(ctx, &f)
	if err != nil {
		return res, err
	}
	from, all, rank := p.from(where, args)
	countCap := snap.Int(SettingSearchCountCap)
	// One row beyond the page tells the two cases apart: a short answer is
	// the whole match, a full one has at least limit+1 matches.
	res.Hits, err = query(ctx, qr, "SELECT m.id, m.source_id, m.store_id, m.msg_id, m.subject, m.received_at, "+rank+from+
		" ORDER BY 7, "+instantSQL+" DESC LIMIT ?", append(all, limit+1), func(r *sql.Rows, h *Hit) error {
		return r.Scan(&h.RowID, &h.SourceID, &h.StoreID, &h.ID, &h.Subject, &h.ReceivedAt, &h.Rank)
	})
	if err != nil {
		return res, err
	}
	if len(res.Hits) <= limit {
		res.Total = len(res.Hits)
		if res.Total > countCap {
			res.Total, res.TotalCapped = countCap, true
		}
		return res, nil
	}
	res.Hits = res.Hits[:limit]
	if limit >= countCap {
		// The page already proves more matches than the cap can report.
		res.Total, res.TotalCapped = countCap, true
		return res, nil
	}
	if err := qr.QueryRowContext(ctx, "SELECT COUNT(*) FROM (SELECT 1"+from+" LIMIT ?)", append(all, countCap+1)...).Scan(&res.Total); err != nil {
		return res, err
	}
	if res.Total > countCap {
		res.Total, res.TotalCapped = countCap, true
	}
	return res, nil
}

// from renders the plan within the filter: the index MATCH (ranked by
// bm25), and each scan term as a substring of the folded fields of a
// message of the substring index (scanTermSQL; rank 0: newest first). A
// strict plan ANDs its terms. A recall plan of scan terms only ORs them;
// with a MATCH it keeps them ANDed, because FTS5 accepts MATCH only as a
// top-level AND condition (a mixed OR is a recorded residual).
func (p ftsPlan) from(where string, args []any) (string, []any, string) {
	var conds []string
	var all []any
	from, rank := " FROM messages m JOIN folders f ON f.id = m.folder_id LEFT JOIN scope_verdicts v ON v.message_id = m.id", "0.0"
	if p.match != "" {
		from = " FROM " + p.table + " JOIN messages m ON m.id = " + p.table + ".rowid JOIN folders f ON f.id = m.folder_id LEFT JOIN scope_verdicts v ON v.message_id = m.id"
		conds, all, rank = append(conds, p.table+" MATCH ?"), append(all, p.match), "bm25("+p.table+")"
		for _, t := range p.scans {
			conds, all = append(conds, scanTermSQL), append(all, t, t, t, t)
		}
	} else if len(p.scans) > 0 {
		conds = append(conds, "m.id IN (SELECT rowid FROM "+p.table+")")
		var scans []string
		for _, t := range p.scans {
			scans, all = append(scans, scanTermSQL), append(all, t, t, t, t)
		}
		if p.anyTerm && len(scans) > 1 {
			conds = append(conds, "("+strings.Join(scans, " OR ")+")")
		} else {
			conds = append(conds, scans...)
		}
	} else {
		conds = append(conds, "m.id IN (SELECT rowid FROM "+p.table+")")
	}
	conds = append(conds, where)
	return from + " WHERE " + strings.Join(conds, " AND "), append(all, args...), rank
}

// Summary is what a search result shows about a message.
type Summary struct {
	RowID          int64           `json:"row_id"`
	SourceID       string          `json:"source_id"`
	StoreID        string          `json:"store_id"`
	ID             string          `json:"id"`
	Subject        string          `json:"subject"`
	SenderName     string          `json:"sender_name"`
	SenderAddress  string          `json:"sender_address"`
	Instant        string          `json:"instant"`
	Role           mail.FolderRole `json:"role"`
	HasAttachments bool            `json:"has_attachments"`
	Body           string          `json:"-"` // snippets are cut from it
}

// Summaries returns the summaries of the given messages (removed ones are
// absent from the map).
func (s *Store) Summaries(ctx context.Context, ids []int64) (map[int64]Summary, error) {
	return s.summaries(ctx, ids, false)
}

// SummariesOwner returns the summaries of the given messages that are
// still visible owner-facing content: a shared, delegate, unattributed,
// unpicked or removed message is absent (never another owner's content under
// a known id).
func (s *Store) SummariesOwner(ctx context.Context, ids []int64) (map[int64]Summary, error) {
	return s.summaries(ctx, ids, true)
}

func (s *Store) summaries(ctx context.Context, ids []int64, ownerOnly bool) (map[int64]Summary, error) {
	out := make(map[int64]Summary, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	from := `FROM messages m JOIN folders f ON f.id = m.folder_id`
	where := `m.id IN ` + idSetSQL
	if ownerOnly {
		vis, err := s.visible(ctx)
		if err != nil {
			return nil, err
		}
		from += ` JOIN scope_verdicts v ON v.message_id = m.id`
		where += ` AND ` + ownerFacingWhere(vis)
	}
	rows, err := query(ctx, s.db, `SELECT m.id, m.source_id, m.store_id, m.msg_id, m.subject, m.sender_name, m.sender_address,
		`+instantSQL+`, f.role, m.has_attachments, m.body `+from+` WHERE `+where, []any{idSet(ids)}, func(r *sql.Rows, v *Summary) error {
		var role string
		err := r.Scan(&v.RowID, &v.SourceID, &v.StoreID, &v.ID, &v.Subject, &v.SenderName, &v.SenderAddress, &v.Instant, &role, &v.HasAttachments, &v.Body)
		v.Role = mail.FolderRole(role)
		return err
	})
	for _, r := range rows {
		out[r.RowID] = r
	}
	return out, err
}

// jsonList is a string list as one JSON parameter (json_each).
func jsonList(items []string) string {
	b, _ := json.Marshal(items) // a []string always encodes
	return string(b)
}

// senderWhere is the one sender predicate of search and the All-mail page,
// over the messages m joined with folders f. An exact address compares the
// stored normalized address; a value with a leading @ (or no @ at all) is a
// domain and matches every address under that one domain, never a subdomain
// and never a sender display name. Both columns are maintained by the one
// write path and indexed with the page order, so a sender page is a range
// read instead of a scan of the whole owner population. An empty sender
// installs no predicate.
// senderFilter is the one shape of the sender filter: the predicate, its
// arguments and the page index that keeps the page a range read. Search
// reads only the predicate; MessageList reads the index too.
type senderFilter struct {
	cond  string
	args  []any
	index string
}

func senderFilterOf(sender string) senderFilter {
	sender = mail.NormalizeAddress(sender)
	switch {
	case sender == "":
		return senderFilter{index: messageListOrderIndex}
	case strings.HasPrefix(sender, "@") || !strings.Contains(sender, "@"):
		return senderFilter{cond: "m.sender_domain = ?", args: []any{strings.TrimPrefix(sender, "@")},
			index: messageListSenderDomainIndex}
	default:
		return senderFilter{cond: "m.sender_norm = ?", args: []any{sender}, index: messageListSenderNormIndex}
	}
}

func senderWhere(sender string) (string, []any) {
	f := senderFilterOf(sender)
	return f.cond, f.args
}

func stringArgs[T ~string](items []T) []any {
	out := make([]any, len(items))
	for i, v := range items {
		out[i] = string(v)
	}
	return out
}
