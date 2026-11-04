package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/LLM-4-People/mailsense/internal/mail"
)

// The FTS indexes are maintained ONLY here: writeFTS replaces the entries
// of a message (rowid = messages.id) inside the caller's transaction. Both
// hold the folded form (mail.Fold, any script); the word index
// (unicode61) every message, the substring index (trigram) the messages
// with letters of a substring-searched script (mail.NeedsSubstringSearch).
const (
	ftsDeleteSQL    = "DELETE FROM messages_fts WHERE rowid = ?"
	ftsInsertSQL    = "INSERT INTO messages_fts (rowid, subject, body, sender, recipients) VALUES (?, ?, ?, ?, ?)"
	ftsTriDeleteSQL = "DELETE FROM messages_fts_tri WHERE rowid = ?"
	ftsTriInsertSQL = "INSERT INTO messages_fts_tri (rowid, subject, body, sender, recipients) VALUES (?, ?, ?, ?, ?)"
	// ftsTextVersion names the folded form both indexes hold. A change
	// re-indexes every message in both indexes.
	ftsTextVersion = "3-nfkc-casefold"
	// ftsCoverageVersion names which messages have a trigram row: version 4
	// added the matra-based Indic scripts to the substring coverage
	// (mail/scripts.json substring_extra), whose dependent vowel signs
	// unicode61 treats as separators. A change rewrites only the trigram
	// coverage; the word index holds the same folded form.
	ftsCoverageVersion = "4-indic-substring"
	// ftsVersion is the legacy single key and its value: older builds read
	// it at open, so it mirrors the newest form for a downgrade. This build
	// reads it only as a fallback when the split keys are absent.
	ftsVersion = ftsCoverageVersion
	// Meta keys of the two versions, plus the legacy mirror.
	metaFTSTextVersion     = "fts_text_version"
	metaFTSCoverageVersion = "fts_coverage_version"
	metaFTSVersion         = "fts_version"
)

// splitLegacyFTSVersion maps the single legacy key to the split versions.
// Version 4 changed only the trigram coverage, version 3 both, and a
// missing, older or unknown value forces a full rebuild of both indexes.
func splitLegacyFTSVersion(v string) (text, coverage string) {
	switch v {
	case ftsCoverageVersion:
		return ftsTextVersion, ftsCoverageVersion
	case ftsTextVersion:
		return ftsTextVersion, ftsTextVersion
	default:
		return "", ""
	}
}

// ftsStmts are the prepared statements of the FTS write path.
type ftsStmts struct{ del, ins, delTri, insTri *sql.Stmt }

func prepareFTS(ctx context.Context, tx *sql.Tx) (ftsStmts, error) {
	var f ftsStmts
	var err error
	for _, p := range []struct {
		st **sql.Stmt
		q  string
	}{{&f.del, ftsDeleteSQL}, {&f.ins, ftsInsertSQL}, {&f.delTri, ftsTriDeleteSQL}, {&f.insTri, ftsTriInsertSQL}} {
		if *p.st, err = tx.PrepareContext(ctx, p.q); err != nil {
			f.close()
			return f, err
		}
	}
	return f, nil
}

// prepareTri prepares only the trigram statements: the coverage-only pass
// never prepares, and so can never run, a word-index statement.
func prepareTri(ctx context.Context, tx *sql.Tx) (ftsStmts, error) {
	var f ftsStmts
	var err error
	for _, p := range []struct {
		st **sql.Stmt
		q  string
	}{{&f.delTri, ftsTriDeleteSQL}, {&f.insTri, ftsTriInsertSQL}} {
		if *p.st, err = tx.PrepareContext(ctx, p.q); err != nil {
			f.close()
			return f, err
		}
	}
	return f, nil
}

func (f ftsStmts) close() {
	for _, st := range []*sql.Stmt{f.del, f.ins, f.delTri, f.insTri} {
		if st != nil {
			_ = st.Close()
		}
	}
}

// ftsFields are the indexed fields of a message, unfolded: subject, body,
// sender (name, address, sent-representing address) and recipients
// (names and addresses). scanTermSQL reads the same fields.
func ftsFields(m *mail.Message) [4]string {
	parts := make([]string, 0, 2*len(m.Recipients))
	for _, r := range m.Recipients {
		parts = append(parts, r.Name, r.Address)
	}
	return [4]string{m.Subject, m.Body, m.SenderName + " " + m.SenderAddress + " " + m.SentRepresentingAddress, strings.Join(parts, " ")}
}

// foldedFTSFields are the bound values of one index row in column order:
// the rowid, then the folded subject, body, sender and recipients. Both
// indexes hold exactly these values.
func foldedFTSFields(id int64, raw [4]string) []any {
	return []any{id, mail.Fold(raw[0]), mail.Fold(raw[1]), mail.Fold(raw[2]), mail.Fold(raw[3])}
}

// insertTri writes one trigram row. writeFTS and reindexTri fold and insert
// through this one helper.
func insertTri(ctx context.Context, f ftsStmts, fields []any) error {
	_, err := f.insTri.ExecContext(ctx, fields...)
	return err
}

func writeFTS(ctx context.Context, f ftsStmts, id int64, m *mail.Message) error {
	if _, err := f.del.ExecContext(ctx, id); err != nil {
		return err
	}
	if _, err := f.delTri.ExecContext(ctx, id); err != nil {
		return err
	}
	raw := ftsFields(m)
	fields := foldedFTSFields(id, raw)
	if _, err := f.ins.ExecContext(ctx, fields...); err != nil {
		return err
	}
	if !needsSubstring(raw) {
		return nil
	}
	return insertTri(ctx, f, fields)
}

// needsSubstring: any indexed field (recipients too) has a letter of a
// substring-searched script.
func needsSubstring(fields [4]string) bool {
	for _, f := range fields {
		if mail.NeedsSubstringSearch(f) {
			return true
		}
	}
	return false
}

// deleteFTS removes a message's entries from both indexes.
func deleteFTS(ctx context.Context, tx *sql.Tx, id int64) error {
	for _, q := range []string{ftsDeleteSQL, ftsTriDeleteSQL} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return err
		}
	}
	return nil
}

// metaValue is one meta row written by the FTS version owner.
type metaValue struct{ key, value string }

// ftsVersionState is the split version state read from meta: the effective
// text and coverage versions (split keys first, the legacy mirror as the
// fallback) and the keys a no-work open still has to seed.
type ftsVersionState struct {
	text, coverage string
	seed           []metaValue
}

func (s *Store) ftsVersionState(ctx context.Context) (ftsVersionState, error) {
	var st ftsVersionState
	have := map[string]string{}
	rows, err := s.db.QueryContext(ctx, "SELECT key, value FROM meta WHERE key IN (?, ?, ?)",
		metaFTSTextVersion, metaFTSCoverageVersion, metaFTSVersion)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return st, err
		}
		have[k] = v
	}
	if err := rows.Err(); err != nil {
		return st, err
	}
	st.text, st.coverage = splitLegacyFTSVersion(have[metaFTSVersion])
	if v, ok := have[metaFTSTextVersion]; ok {
		st.text = v
	}
	if v, ok := have[metaFTSCoverageVersion]; ok {
		st.coverage = v
	}
	// Only a fully current pair seeds: a stale side is overwritten by the
	// rebuild that it forces.
	if st.text == ftsTextVersion && st.coverage == ftsCoverageVersion {
		if _, ok := have[metaFTSTextVersion]; !ok {
			st.seed = append(st.seed, metaValue{metaFTSTextVersion, ftsTextVersion})
		}
		if _, ok := have[metaFTSCoverageVersion]; !ok {
			st.seed = append(st.seed, metaValue{metaFTSCoverageVersion, ftsCoverageVersion})
		}
		if v, ok := have[metaFTSVersion]; !ok || v != ftsVersion {
			st.seed = append(st.seed, metaValue{metaFTSVersion, ftsVersion})
		}
	}
	return st, nil
}

// writeFTSVersions records the split versions and the legacy mirror in one
// transaction: a downgrade reads the mirror and must see either the old
// state or the completed new one.
func (s *Store) writeFTSVersions(ctx context.Context, values ...metaValue) error {
	if len(values) == 0 {
		return nil
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, v := range values {
			if err := putMeta(ctx, tx, v.key, v.value); err != nil {
				return err
			}
		}
		return nil
	})
}

// reindexFTS brings the indexes and the meta versions up to date at open. A
// stale folded-text version rewrites both indexes; a current folded form
// with stale trigram coverage scans the messages and rewrites only the
// trigram rows; a current pair seeds missing split keys and does no index
// work.
func (s *Store) reindexFTS(ctx context.Context) error {
	st, err := s.ftsVersionState(ctx)
	if err != nil {
		return err
	}
	switch {
	case st.text != ftsTextVersion:
		return s.reindexFull(ctx)
	case st.coverage != ftsCoverageVersion:
		return s.reindexTriOnly(ctx)
	default:
		return s.writeFTSVersions(ctx, st.seed...)
	}
}

// reindexFull rewrites both indexes, in transactions of sync.batch_size
// messages (the store's one write batch); the versions are recorded once
// all are done.
func (s *Store) reindexFull(ctx context.Context) error {
	start, n := s.clock.Now(), 0
	if err := eachIDBatch(ctx, s.db, s.cfg.Int(SettingSyncBatchSize), func(ids []int64) error {
		if err := s.inTx(ctx, func(tx *sql.Tx) error { return reindexBatch(ctx, tx, ids) }); err != nil {
			return err
		}
		n += len(ids)
		return nil
	}); err != nil {
		return err
	}
	if err := s.writeFTSVersions(ctx,
		metaValue{metaFTSTextVersion, ftsTextVersion},
		metaValue{metaFTSCoverageVersion, ftsCoverageVersion},
		metaValue{metaFTSVersion, ftsVersion}); err != nil {
		return err
	}
	s.log.Info("full-text word index rebuilt", "version", ftsTextVersion, "messages", n, "scanned", n, "inserted", n, "deleted", n,
		"duration", s.clock.Now().Sub(start))
	// The pre-split line is kept for operator docs and log parsers that
	// quote it; it describes the same full rebuild.
	s.log.Info("full-text indexes rebuilt", "version", ftsVersion, "messages", n, "duration", s.clock.Now().Sub(start))
	return nil
}

// triReindexResult is the work and time of one trigram coverage pass. The
// scan covers the message reads, recipient parsing and the coverage
// predicate; the write covers folding and the trigram inserts and deletes.
type triReindexResult struct {
	scanned, inserted, deleted int
	scan, write                time.Duration
}

// reindexTriOnly rewrites the trigram coverage and records it after the
// whole pass: an interrupted pass leaves the old coverage version, so the
// next open retries and finds the rows it already inserted.
func (s *Store) reindexTriOnly(ctx context.Context) error {
	start := s.clock.Now()
	res, err := s.reindexTri(ctx)
	if err != nil {
		return err
	}
	if err := s.writeFTSVersions(ctx,
		metaValue{metaFTSTextVersion, ftsTextVersion},
		metaValue{metaFTSCoverageVersion, ftsCoverageVersion},
		metaValue{metaFTSVersion, ftsVersion}); err != nil {
		return err
	}
	s.log.Info("full-text trigram index rebuilt", "version", ftsCoverageVersion, "scanned", res.scanned,
		"inserted", res.inserted, "deleted", res.deleted, "duration", s.clock.Now().Sub(start))
	return nil
}

// reindexTri scans every message for the substring coverage predicate,
// inserts only the missing trigram rows, marks the needed ones seen and
// deletes the remaining stale rows at the end. It never prepares or runs a
// word-index statement, and an existing row is not rewritten: the word and
// trigram content of a message do not change when only the coverage does.
func (s *Store) reindexTri(ctx context.Context) (triReindexResult, error) {
	var res triReindexResult
	existing := map[int64]struct{}{}
	rows, err := s.db.QueryContext(ctx, "SELECT rowid FROM messages_fts_tri")
	if err != nil {
		return res, err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return res, err
		}
		existing[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return res, err
	}
	if err := rows.Close(); err != nil {
		return res, err
	}
	seen := make(map[int64]struct{}, len(existing))
	batch := s.cfg.Int(SettingSyncBatchSize)
	if err := eachIDBatch(ctx, s.db, batch, func(ids []int64) error {
		inserted := 0
		if err := s.inTx(ctx, func(tx *sql.Tx) error {
			f, err := prepareTri(ctx, tx)
			if err != nil {
				return err
			}
			defer f.close()
			for _, id := range ids {
				scanStart := s.clock.Now()
				m, err := readFTSMessage(ctx, tx, id)
				if err != nil {
					return err
				}
				raw := ftsFields(&m)
				needed := needsSubstring(raw)
				res.scan += s.clock.Now().Sub(scanStart)
				if !needed {
					continue
				}
				seen[id] = struct{}{}
				if _, ok := existing[id]; ok {
					continue
				}
				writeStart := s.clock.Now()
				if err := insertTri(ctx, f, foldedFTSFields(id, raw)); err != nil {
					return err
				}
				res.write += s.clock.Now().Sub(writeStart)
				inserted++
			}
			return nil
		}); err != nil {
			return err
		}
		res.scanned += len(ids)
		res.inserted += inserted
		return nil
	}); err != nil {
		return res, err
	}
	var stale []int64
	for id := range existing {
		if _, ok := seen[id]; !ok {
			stale = append(stale, id)
		}
	}
	if len(stale) > 0 {
		slices.Sort(stale)
		for len(stale) > 0 {
			n := min(batch, len(stale))
			chunk := stale[:n]
			stale = stale[n:]
			writeStart := s.clock.Now()
			if err := s.inTx(ctx, func(tx *sql.Tx) error {
				f, err := prepareTri(ctx, tx)
				if err != nil {
					return err
				}
				defer f.close()
				for _, id := range chunk {
					if _, err := f.delTri.ExecContext(ctx, id); err != nil {
						return err
					}
					res.deleted++
				}
				return nil
			}); err != nil {
				return res, err
			}
			res.write += s.clock.Now().Sub(writeStart)
		}
	}
	return res, nil
}

func reindexBatch(ctx context.Context, tx *sql.Tx, ids []int64) error {
	f, err := prepareFTS(ctx, tx)
	if err != nil {
		return err
	}
	defer f.close()
	for _, id := range ids {
		m, err := readFTSMessage(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := writeFTS(ctx, f, id, &m); err != nil {
			return err
		}
	}
	return nil
}

// eachIDBatch walks the message ids in ascending order, in batches of size,
// and hands each batch to fn. It is the one keyset walker of the full-text
// rebuilds: a new pass cannot reintroduce its own id scan, and both passes
// read the same rows in the same order.
func eachIDBatch(ctx context.Context, q queryer, size int, fn func(ids []int64) error) error {
	for after := int64(0); ; {
		ids, err := query(ctx, q, "SELECT id FROM messages WHERE id > ? ORDER BY id LIMIT ?",
			[]any{after, size}, func(r *sql.Rows, v *int64) error { return r.Scan(v) })
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		if err := fn(ids); err != nil {
			return err
		}
		after = ids[len(ids)-1]
	}
}

// ftsMessageSQL is the one projection the full-text writers read: the fields
// ftsFields folds plus the serialized recipients.
const ftsMessageSQL = `SELECT subject, body, sender_name, sender_address, sent_representing_address, ` + recipientsSQL + `
	FROM messages m WHERE id = ?`

// readFTSMessage reads one message for a full-text write, on the pool or
// inside the writer's transaction. Both the word index and the trigram
// index read through it, so a field added to one write reaches both.
func readFTSMessage(ctx context.Context, q queryer, id int64) (mail.Message, error) {
	var m mail.Message
	var recips string
	if err := q.QueryRowContext(ctx, ftsMessageSQL, id).Scan(&m.Subject, &m.Body, &m.SenderName,
		&m.SenderAddress, &m.SentRepresentingAddress, &recips); err != nil {
		return mail.Message{}, err
	}
	recipients, err := parseRecipients(recips)
	if err != nil {
		return mail.Message{}, err
	}
	m.Recipients = recipients
	return m, nil
}

// Hit is one full-text match.
type Hit struct {
	RowID      int64   `json:"row_id"`
	SourceID   string  `json:"source_id"`
	StoreID    string  `json:"store_id"`
	ID         string  `json:"id"`
	Subject    string  `json:"subject"`
	ReceivedAt string  `json:"received_at"`
	Rank       float64 `json:"rank"`
}

// SearchResult carries the top hits, the match count (capped at
// search.count_cap: counting every match of a common word over a million
// messages costs more than the search) and how each query term matched.
type SearchResult struct {
	Hits        []Hit       `json:"hits"`
	Total       int         `json:"total"`
	TotalCapped bool        `json:"total_capped"` // Total is the cap; there are more
	Terms       []TermMatch `json:"terms"`
}

// TermMode is how a query term is matched (disclosed with every result).
type TermMode string

// Term modes.
const (
	// ModeWord: a whole word of the word index (scripts with spaces).
	ModeWord TermMode = "word"
	// ModeSubstring: a substring of the substring index (a query with a
	// term in a substring-searched script: Han, Kana, Thai, Hangul, the
	// matra-based Indic scripts…).
	ModeSubstring TermMode = "substring"
	// ModeScan: a substring index term shorter than three characters
	// (trigrams cannot hold it): the messages of the substring index are
	// scanned for it, slower and disclosed.
	ModeScan TermMode = "scan"
)

// TermMatch is one query term and its mode.
type TermMatch struct {
	Term string   `json:"term"`
	Mode TermMode `json:"mode"`
}

// ErrEmptySearch reports a query with nothing searchable left after folding
// and parsing (empty, quotes only, or only characters FTS cannot index). The
// search service maps it to its client-facing empty-query error.
var ErrEmptySearch = errors.New("store: empty search")

// ftsPlan is a query turned into index expressions: every parsed term is a
// quoted phrase of the folded form (operators and syntax neutralised), all
// ANDed by default or ORed for a recall query. A query with a
// substring-searched-script term runs on the substring index, where terms
// shorter than a trigram are scanned.
type ftsPlan struct {
	table   string // messages_fts or messages_fts_tri
	match   string // the MATCH expression ("" = scan terms only)
	scans   []string
	terms   []TermMatch
	anyTerm bool // recall query (SearchTextAny)
}

// queryPart is one parsed term: a bare word (one FTS token) or a quoted span
// (one FTS phrase, its words adjacent and ordered). Both are quoted in the
// MATCH expression; the span's spaces are what keep it a phrase.
type queryPart struct{ text string }

// planQuery folds the whole query once, then parses ASCII quotes into bare
// words and quoted spans. Folding first normalises compatibility forms
// (fullwidth ＂ becomes "), removes default ignorables (a zero-width space)
// and lowercases every script, so the parser sees one canonical string.
// Control characters, NUL included, become spaces: they are separators, not
// FTS syntax. A quote that is never closed runs to the end as a phrase.
func planQuery(text string, anyTerm bool) (ftsPlan, error) {
	parts, err := parseQuery(text)
	if err != nil {
		return ftsPlan{}, err
	}
	tri := false
	for _, part := range parts {
		tri = tri || mail.NeedsSubstringSearch(part.text)
	}
	p := ftsPlan{table: "messages_fts", anyTerm: anyTerm}
	if tri {
		p.table = "messages_fts_tri"
	}
	var phrases []string
	for _, part := range parts {
		switch {
		case !tri:
			phrases, p.terms = append(phrases, quoteFTS(part.text)), append(p.terms, TermMatch{part.text, ModeWord})
		case utf8.RuneCountInString(part.text) >= 3:
			phrases, p.terms = append(phrases, quoteFTS(part.text)), append(p.terms, TermMatch{part.text, ModeSubstring})
		default:
			p.scans, p.terms = append(p.scans, part.text), append(p.terms, TermMatch{part.text, ModeScan})
		}
	}
	sep := " "
	if anyTerm {
		sep = " OR "
	}
	p.match = strings.Join(phrases, sep)
	return p, nil
}

// parseQuery is the one query parser of both indexes. Bare words split on
// whitespace; an ASCII quote opens or closes one span. Every span becomes one
// term, so a quoted phrase stays a phrase. Quotes are delimiters, never part
// of a term, and an empty result is the typed ErrEmptySearch.
func parseQuery(text string) ([]queryPart, error) {
	folded := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, mail.Fold(text))
	var parts []queryPart
	var word strings.Builder
	flush := func() {
		if term := strings.TrimSpace(word.String()); term != "" {
			parts = append(parts, queryPart{text: term})
		}
		word.Reset()
	}
	inQuote := false
	for _, r := range folded {
		switch {
		case r == '"':
			flush()
			inQuote = !inQuote
		case inQuote:
			word.WriteRune(r)
		case unicode.IsSpace(r):
			flush()
		default:
			word.WriteRune(r)
		}
	}
	flush() // an unclosed quote runs to the end as a phrase
	if len(parts) == 0 {
		return nil, ErrEmptySearch
	}
	return parts, nil
}

// quoteFTS wraps one parsed term in FTS phrase quotes. The parser removed
// every ASCII quote, so no other syntax character can escape the phrase.
func quoteFTS(term string) string { return `"` + term + `"` }

// Search is SearchText over all mail of active folders.
func (s *Store) Search(ctx context.Context, text string, limit int) (SearchResult, error) {
	return s.SearchText(ctx, text, SearchFilter{}, limit)
}
