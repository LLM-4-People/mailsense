package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/runstate"
)

// MaxPermanentFailures is the safety bound of the permanent-failure
// quarantine: a message the provider rejects for itself (engine.ErrRejected)
// at the same revision and answer context is left out of the automatic plan
// after this many attempts. It is a safety bound, not a setting: the user
// clears it by changing an input (message revision, profile fingerprint,
// decider, route or preview) or with a manual Force/Redecide, and a manual
// decision write clears the failure row.
const MaxPermanentFailures = 3

// TriageKey is one decision space: a question spec and a decider
// (provider/model). Results of another key are never mixed in.
type TriageKey struct {
	Fingerprint string `json:"fingerprint"`
	Decider     string `json:"decider"`
}

// Decision is one cached raw answer set. Revision is the message's
// revision it was decided at: a paid decision stays valid for its message
// until that revision changes (triage planning).
type Decision struct {
	MessageID int64  `json:"message_id"`
	Revision  int64  `json:"message_revision"`
	StateHash string `json:"state_hash"`
	Answers   string `json:"answers"` // JSON map question → engine.Answer
	RunID     int64  `json:"run_id"`
	DecidedAt string `json:"decided_at"`
	// KeepFailure leaves the message's failure row in place: a partial answer
	// set saved by an attempt that also failed does not clear the failure
	// (and its attempt count). A complete or manual decision clears it.
	KeepFailure bool `json:"-"`
}

// Failure is the last failure of a message.
type Failure struct {
	MessageID int64  `json:"message_id"`
	Revision  int64  `json:"message_revision"` // the message revision the attempt read
	Error     string `json:"error"`
	RunID     int64  `json:"run_id"`
	FailedAt  string `json:"failed_at"`
	// Attempts counts consecutive failures of the same class at the same
	// revision and context; a class, revision or context change starts at 1.
	Attempts  int  `json:"attempts"`
	Permanent bool `json:"permanent"` // the provider rejected this message itself (engine.ErrRejected)
	// Context is the answer-source identity the attempt ran under (route key
	// and preview flag): a change makes the failure retryable again.
	Context string `json:"context"`
}

// Quarantined reports whether this failure leaves its message out of the
// automatic plan: a permanent per-message rejection, at the message's current
// revision and answer context, repeated MaxPermanentFailures times. A
// transient or provider-wide failure never quarantines. It is the per-row
// form of failureQuarantineSQL (the planner's SQL form).
func (f Failure) Quarantined(revision int64, context string) bool {
	return f.Permanent && f.Attempts >= MaxPermanentFailures && f.Revision == revision && f.Context == context
}

// SaveDecision stores a message's raw answers and clears its failure
// (unless Decision.KeepFailure marks a partial save of a failed attempt).
func (s *Store) SaveDecision(ctx context.Context, k TriageKey, d Decision) error {
	return s.SaveDecisions(ctx, k, []Decision{d})
}

// SaveDecisions stores many decisions of one key in one transaction:
// each replaces the message's decision and clears its failure unless the
// decision keeps it.
func (s *Store) SaveDecisions(ctx context.Context, k TriageKey, ds []Decision) error {
	return s.busy.WithBusyRetry(ctx, "save triage decisions", func() error {
		return s.inTx(ctx, func(tx *sql.Tx) error { return s.saveDecisions(ctx, tx, k, ds) })
	})
}

// ReplaceDecisions makes ds the key's only decisions, in one transaction
// (an import replaced as a whole: a failure keeps the previous ones).
func (s *Store) ReplaceDecisions(ctx context.Context, k TriageKey, ds []Decision) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM triage_decisions WHERE fingerprint = ? AND decider = ?", k.Fingerprint, k.Decider); err != nil {
			return err
		}
		return s.saveDecisions(ctx, tx, k, ds)
	})
}

func (s *Store) saveDecisions(ctx context.Context, tx *sql.Tx, k TriageKey, ds []Decision) error {
	return s.streamDecisions(ctx, tx, k, false, func(fn func(Decision) error) error {
		for _, d := range ds {
			if err := fn(d); err != nil {
				return err
			}
		}
		return nil
	})
}

// streamDecisions writes the decisions each yields, inside tx. A row whose
// content is unchanged is not rewritten: a re-run of the same models on the
// same mail commits almost nothing, and the WAL stays small. prune makes
// the stream a whole-key replacement (a layer-1 commit): the rows it did
// not visit are deleted. A partial save (a teacher's few answers) never
// prunes: its stream is not the key's population.
func (s *Store) streamDecisions(ctx context.Context, tx *sql.Tx, k TriageKey, prune bool, each func(fn func(Decision) error) error) error {
	var seen *seenRows
	if prune {
		var err error
		if seen, err = newSeen(ctx, tx, "seen_decisions"); err != nil {
			return err
		}
	}
	ins, err := tx.PrepareContext(ctx, insertDecisionSQL)
	if err != nil {
		return err
	}
	defer ins.Close()
	del, err := tx.PrepareContext(ctx, "DELETE FROM triage_failures WHERE message_id = ? AND fingerprint = ? AND decider = ?")
	if err != nil {
		return err
	}
	defer del.Close()
	now := s.now()
	if err := each(func(d Decision) error {
		if _, err := ins.ExecContext(ctx, d.MessageID, k.Fingerprint, k.Decider, d.StateHash, d.Answers, d.RunID, now, d.Revision, d.MessageID); err != nil {
			return fmt.Errorf("store: save decision %d: %w", d.MessageID, err)
		}
		if seen != nil {
			if err := seen.add(ctx, tx, d.MessageID); err != nil {
				return err
			}
		}
		if d.KeepFailure {
			return nil
		}
		_, err := del.ExecContext(ctx, d.MessageID, k.Fingerprint, k.Decider)
		return err
	}); err != nil {
		return err
	}
	if seen != nil {
		_, err = seen.deleteStale(ctx, tx, "triage_decisions", "fingerprint = ? AND decider = ?", k.Fingerprint, k.Decider)
		return err
	}
	return nil
}

// insertDecisionSQL is the one decision upsert: a row whose content is
// unchanged is not rewritten, which keeps a no-op run's WAL small.
const insertDecisionSQL = `INSERT INTO triage_decisions (message_id, fingerprint, decider, state_hash, answers, run_id, decided_at, message_revision)
	SELECT ?, ?, ?, ?, ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM messages WHERE id = ?)
	ON CONFLICT(message_id, fingerprint, decider) DO UPDATE SET state_hash = excluded.state_hash,
	answers = excluded.answers, run_id = excluded.run_id, decided_at = excluded.decided_at, message_revision = excluded.message_revision
	WHERE excluded.state_hash <> triage_decisions.state_hash OR excluded.answers <> triage_decisions.answers
	OR excluded.message_revision <> triage_decisions.message_revision`

// DecisionSummary is how many decisions a key holds and when the last
// was made (a cheap change marker of a teacher).
func (s *Store) DecisionSummary(ctx context.Context, k TriageKey) (count int, last string, err error) {
	err = s.db.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(MAX(decided_at), '') FROM triage_decisions WHERE fingerprint = ? AND decider = ?",
		k.Fingerprint, k.Decider).Scan(&count, &last)
	return count, last, err
}

// SaveFailure records a message failure; an earlier decision is kept. A
// failure of the same class at the same revision and answer context adds one
// attempt; any change starts the count again at one, so a quarantine never
// survives an input change.
func (s *Store) SaveFailure(ctx context.Context, k TriageKey, f Failure) error {
	return s.busy.WithBusyRetry(ctx, "save triage failure", func() error {
		_, err := s.db.ExecContext(ctx, `INSERT INTO triage_failures
			(message_id, fingerprint, decider, error, run_id, failed_at, message_revision, attempts, permanent, context)
		SELECT ?, ?, ?, ?, ?, ?, ?, 1, ?, ? WHERE EXISTS (SELECT 1 FROM messages WHERE id = ?)
		ON CONFLICT(message_id, fingerprint, decider) DO UPDATE SET
			attempts = CASE WHEN excluded.message_revision = triage_failures.message_revision
				AND excluded.permanent = triage_failures.permanent
				AND excluded.context = triage_failures.context
				THEN triage_failures.attempts + 1 ELSE 1 END,
			error = excluded.error, run_id = excluded.run_id, failed_at = excluded.failed_at,
			message_revision = excluded.message_revision, permanent = excluded.permanent,
			context = excluded.context`,
			f.MessageID, k.Fingerprint, k.Decider, f.Error, f.RunID, s.now(), f.Revision, f.Permanent, f.Context, f.MessageID)
		return err
	})
}

// decisionColumnNames is the one column list of a stored decision.
var decisionColumnNames = []string{"message_id", "message_revision", "state_hash", "answers", "run_id", "decided_at"}

// decisionSelect is the decision column list in scanDecision order,
// optionally qualified with a table alias.
func decisionSelect(prefix string) string {
	parts := make([]string, len(decisionColumnNames))
	for i, n := range decisionColumnNames {
		parts[i] = prefix + n
	}
	return strings.Join(parts, ", ")
}

// scanDecision reads one decision row in decisionSelect order.
func scanDecision(r *sql.Rows, d *Decision) error {
	return r.Scan(&d.MessageID, &d.Revision, &d.StateHash, &d.Answers, &d.RunID, &d.DecidedAt)
}

// failureColumnNames is the one column list of a stored failure.
var failureColumnNames = []string{"message_id", "message_revision", "error", "run_id", "failed_at", "attempts", "permanent", "context"}

// failureSelect is the failure column list in scanFailure order, optionally
// qualified with a table alias.
func failureSelect(prefix string) string {
	parts := make([]string, len(failureColumnNames))
	for i, n := range failureColumnNames {
		parts[i] = prefix + n
	}
	return strings.Join(parts, ", ")
}

// scanFailure reads one failure row in failureSelect order.
func scanFailure(r *sql.Rows, f *Failure) error {
	return r.Scan(&f.MessageID, &f.Revision, &f.Error, &f.RunID, &f.FailedAt, &f.Attempts, &f.Permanent, &f.Context)
}

// LoadDecisions returns every cached decision of a key by message row id.
func (s *Store) LoadDecisions(ctx context.Context, k TriageKey) (map[int64]Decision, error) {
	out := map[int64]Decision{}
	err := s.EachDecision(ctx, k, func(d Decision) error {
		out[d.MessageID] = d
		return nil
	})
	return out, err
}

// EachDecision streams a key's decisions in message order without holding
// them all in memory (layer 1 reads a whole mailbox's answers); fn
// returning an error stops. fn must not use the store.
func (s *Store) EachDecision(ctx context.Context, k TriageKey, fn func(Decision) error) error {
	rows, err := s.db.QueryContext(ctx, `SELECT `+decisionSelect("")+` FROM triage_decisions
		WHERE fingerprint = ? AND decider = ? ORDER BY message_id`, k.Fingerprint, k.Decider)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var d Decision
		if err := scanDecision(rows, &d); err != nil {
			return err
		}
		if err := fn(d); err != nil {
			return err
		}
	}
	return rows.Err()
}

// DecisionScope is the population and decision space of one triage plan:
// the decision key, the message filter (roles, owner scope, row ids), the
// picked stores the filter cannot name, and the profile's questions. Force
// and Redecide are the run's explicit opinions: both make every eligible
// message a candidate, because a forced run re-decides fresh messages and a
// re-decide run may refresh stale ones.
type DecisionScope struct {
	Key       TriageKey
	Filter    MessageFilter
	Stores    []string
	Questions []QuestionKey
	Force     bool
	Redecide  bool
	// Context is the answer-source identity a stored permanent failure must
	// match to quarantine its message (route key and preview flag). A change
	// makes the message a candidate again.
	Context string
}

// decisionCandidateSQL is the one SQL definition of a stored decision that
// cannot serve its message now: never decided, decided at another message
// revision, or a profile answer that is missing, has another type, or is
// not an engine.Answer that encoding/json can read back. d is the LEFT
// JOINed decision row; the bind parameter is the JSON object of question
// name → type (bound once per EXISTS). Answers outside the profile are ignored by both
// this predicate and decodeAnswers; the answer-shape checks are the JSON
// types encoding/json accepts for engine.Answer's fields. A decision
// outside this predicate is current for the message revision, so no
// default run re-decides it (its derived-data staleness is disclosed by
// Plan/Read, never pending on its own). Force and Redecide widen the
// population to every eligible message in DecisionCandidates instead.
//
// A candidate is still not pending when failureQuarantineSQL holds: a
// message the provider rejected for itself keeps failing on the same input,
// so the automatic plan leaves it out after MaxPermanentFailures attempts.
// The failure row stays stored (Failed coverage still counts it).
const decisionCandidateSQL = `(d.message_id IS NULL
	OR d.message_revision <> m.revision
	OR json_type(CASE WHEN json_valid(d.answers) THEN d.answers END) IS NOT 'object'
	OR EXISTS (
		SELECT 1 FROM json_each(?) q
		WHERE json_extract(CASE WHEN json_valid(d.answers) THEN d.answers END,
			'$.' || json_quote(q.key) || '.type') IS NOT q.value)
	OR EXISTS (
		SELECT 1 FROM json_each(CASE WHEN json_valid(d.answers) THEN d.answers END) a
		JOIN json_each(?) q ON q.key = a.key
		WHERE a.type <> 'object'
			OR json_type((CASE WHEN a.type = 'object' THEN a.value END) -> '$.refused') NOT IN ('null', 'true', 'false')
			OR json_type((CASE WHEN a.type = 'object' THEN a.value END) -> '$.value') NOT IN ('null', 'integer', 'real')
			OR json_type((CASE WHEN a.type = 'object' THEN a.value END) -> '$.choice') NOT IN ('null', 'text')
			OR json_type((CASE WHEN a.type = 'object' THEN a.value END) -> '$.probabilities') NOT IN ('null', 'object')
			OR EXISTS (
				SELECT 1 FROM json_each(CASE WHEN a.type = 'object' THEN a.value END, '$.probabilities') p
				WHERE json_type((CASE WHEN a.type = 'object' THEN a.value END) -> ('$.probabilities.' || json_quote(p.key)))
					NOT IN ('null', 'integer', 'real'))
			OR json_type((CASE WHEN a.type = 'object' THEN a.value END) -> '$.confidence') NOT IN ('null', 'integer', 'real')
			OR json_type((CASE WHEN a.type = 'object' THEN a.value END) -> '$.abstain') NOT IN ('null', 'text')))`

// failureQuarantineSQL is the one quarantine predicate: a permanent
// per-message rejection recorded at the message's current revision, under the
// current answer context, repeated MaxPermanentFailures times. tf is the
// LEFT JOINed failure row; the binds are the attempt bound and the context.
// Transient and provider-wide failures are permanent = 0 and never match.
const failureQuarantineSQL = `(tf.message_id IS NOT NULL AND tf.permanent <> 0
	AND tf.attempts >= ? AND tf.message_revision = m.revision AND tf.context = ?)`

// questionsJSON binds a question set as one JSON object ({name: type}), the
// form decisionCandidateSQL reads with json_each.
func questionsJSON(qs []QuestionKey) (string, error) {
	byName := make(map[string]string, len(qs))
	for _, q := range qs {
		byName[q.Name] = q.Type
	}
	raw, err := json.Marshal(byName)
	if err != nil {
		return "", fmt.Errorf("store: decision questions: %w", err)
	}
	return string(raw), nil
}

// decisionScopeSQL returns the FROM and WHERE of one decision scope over
// the visible message population and its filter arguments. It is the one
// population definition of the probe, the counts and both decision loads.
func (s *Store) decisionScopeSQL(ctx context.Context, sc DecisionScope) (from, where string, args []any, err error) {
	vis, err := s.visible(ctx)
	if err != nil {
		return "", "", nil, err
	}
	where, args = sc.Filter.sql(vis)
	if len(sc.Stores) > 0 {
		where += " AND mailsense_store_key(m.source_id, m.store_id) IN " + stringSetSQL
		args = append(args, jsonList(sc.Stores))
	}
	from = ` FROM messages m JOIN folders f ON f.id = m.folder_id LEFT JOIN scope_verdicts v ON v.message_id = m.id`
	return from, where, args, nil
}

// DecisionCount counts the eligible messages of a scope.
func (s *Store) DecisionCount(ctx context.Context, sc DecisionScope) (int, error) {
	from, where, args, err := s.decisionScopeSQL(ctx, sc)
	if err != nil {
		return 0, err
	}
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*)"+from+" WHERE "+where, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count decision population: %w", err)
	}
	return n, nil
}

// DecisionCandidates lists the eligible message row ids that may need a
// decision under sc, in row-id order. Under Force or Redecide every
// eligible message is a candidate; otherwise only the rows
// decisionCandidateSQL selects that are not quarantined by
// failureQuarantineSQL. The predicate is the probe's: a run that starts on a
// hit re-reads nothing the probe did not call a candidate.
func (s *Store) DecisionCandidates(ctx context.Context, sc DecisionScope) ([]int64, error) {
	from, where, args, err := s.decisionScopeSQL(ctx, sc)
	if err != nil {
		return nil, err
	}
	if sc.Force || sc.Redecide {
		return s.scopeRowIDs(ctx, from, where, args)
	}
	questions, err := questionsJSON(sc.Questions)
	if err != nil {
		return nil, err
	}
	q := "SELECT m.id" + from + decisionJoinSQL + `
		WHERE ` + where + " AND " + decisionCandidateSQL + " AND NOT " + failureQuarantineSQL + " ORDER BY m.id"
	qargs := decisionJoinArgs(sc, args, questions)
	qargs = append(qargs, MaxPermanentFailures, sc.Context)
	ids, err := query(ctx, s.db, q, qargs, scanRowID)
	if err != nil {
		return nil, fmt.Errorf("store: decision candidates: %w", err)
	}
	if ids == nil {
		ids = []int64{}
	}
	return ids, nil
}

// decisionJoinSQL joins a scope's messages to their decision and failure rows
// of the scope key. It is the one join of every pending and quarantine query.
const decisionJoinSQL = ` LEFT JOIN triage_decisions d ON d.message_id = m.id AND d.fingerprint = ? AND d.decider = ?
	LEFT JOIN triage_failures tf ON tf.message_id = m.id AND tf.fingerprint = ? AND tf.decider = ?`

// decisionJoinArgs binds decisionJoinSQL's key and then the scope arguments
// and the question set (used twice by decisionCandidateSQL).
func decisionJoinArgs(sc DecisionScope, args []any, questions string) []any {
	out := []any{sc.Key.Fingerprint, sc.Key.Decider, sc.Key.Fingerprint, sc.Key.Decider}
	out = append(out, args...)
	return append(out, questions, questions)
}

// DecisionQuarantinedCount counts the eligible messages that would be
// candidates but are held back by failureQuarantineSQL. It is the plan's
// quarantine count: those messages are not pending, and they are not fresh
// either, so a plan never reports them as decided.
func (s *Store) DecisionQuarantinedCount(ctx context.Context, sc DecisionScope) (int, error) {
	if sc.Force || sc.Redecide {
		return 0, nil
	}
	from, where, args, err := s.decisionScopeSQL(ctx, sc)
	if err != nil {
		return 0, err
	}
	questions, err := questionsJSON(sc.Questions)
	if err != nil {
		return 0, err
	}
	q := "SELECT COUNT(*)" + from + decisionJoinSQL + `
		WHERE ` + where + " AND " + decisionCandidateSQL + " AND " + failureQuarantineSQL
	qargs := decisionJoinArgs(sc, args, questions)
	qargs = append(qargs, MaxPermanentFailures, sc.Context)
	var n int
	if err := s.db.QueryRowContext(ctx, q, qargs...).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: quarantined decisions: %w", err)
	}
	return n, nil
}

// scopeRowIDs lists every eligible message row of a scope in row-id order.
func (s *Store) scopeRowIDs(ctx context.Context, from, where string, args []any) ([]int64, error) {
	ids, err := query(ctx, s.db, "SELECT m.id"+from+" WHERE "+where+" ORDER BY m.id", args, scanRowID)
	if err != nil {
		return nil, fmt.Errorf("store: decision population rows: %w", err)
	}
	if ids == nil {
		ids = []int64{}
	}
	return ids, nil
}

func scanRowID(r *sql.Rows, id *int64) error { return r.Scan(id) }

// HasPendingDecisions answers whether at least one eligible message of sc
// may need a decision, with one EXISTS ... LIMIT 1: it transfers no
// decision row into Go and composes no state. It is the trigger's cheap
// no-work gate.
func (s *Store) HasPendingDecisions(ctx context.Context, sc DecisionScope) (bool, error) {
	from, where, args, err := s.decisionScopeSQL(ctx, sc)
	if err != nil {
		return false, err
	}
	q := "SELECT EXISTS(SELECT 1" + from
	qargs := args
	if sc.Force || sc.Redecide {
		q += " WHERE " + where
	} else {
		questions, err := questionsJSON(sc.Questions)
		if err != nil {
			return false, err
		}
		q += decisionJoinSQL + ` WHERE ` + where + " AND " + decisionCandidateSQL + " AND NOT " + failureQuarantineSQL
		qargs = decisionJoinArgs(sc, args, questions)
		qargs = append(qargs, MaxPermanentFailures, sc.Context)
	}
	q += " LIMIT 1)"
	var ok bool
	if err := s.db.QueryRowContext(ctx, q, qargs...).Scan(&ok); err != nil {
		return false, fmt.Errorf("store: pending decision probe: %w", err)
	}
	return ok, nil
}

// LoadScopeDecisions loads the decisions of every eligible message of a
// scope, keyed by row id. It never reads a decision of a message outside
// the population.
func (s *Store) LoadScopeDecisions(ctx context.Context, sc DecisionScope) (map[int64]Decision, error) {
	from, where, args, err := s.decisionScopeSQL(ctx, sc)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + decisionSelect("d.") + from + `
		JOIN triage_decisions d ON d.message_id = m.id AND d.fingerprint = ? AND d.decider = ?
		WHERE ` + where + ` ORDER BY d.message_id`
	qargs := append([]any{sc.Key.Fingerprint, sc.Key.Decider}, args...)
	list, err := query(ctx, s.db, q, qargs, scanDecision)
	if err != nil {
		return nil, fmt.Errorf("store: scope decisions: %w", err)
	}
	out := make(map[int64]Decision, len(list))
	for _, d := range list {
		out[d.MessageID] = d
	}
	return out, nil
}

// LoadDecisionsFor loads the decisions of the given message rows, keyed by
// row id. An empty list reads nothing.
func (s *Store) LoadDecisionsFor(ctx context.Context, k TriageKey, ids []int64) (map[int64]Decision, error) {
	out := map[int64]Decision{}
	if len(ids) == 0 {
		return out, nil
	}
	list, err := query(ctx, s.db, `SELECT `+decisionSelect("")+` FROM triage_decisions
		WHERE fingerprint = ? AND decider = ? AND message_id IN `+idSetSQL+` ORDER BY message_id`,
		[]any{k.Fingerprint, k.Decider, idSet(ids)}, scanDecision)
	if err != nil {
		return nil, fmt.Errorf("store: load decisions: %w", err)
	}
	for _, d := range list {
		out[d.MessageID] = d
	}
	return out, nil
}

// LoadFailures returns the current failures of a key by message row id.
func (s *Store) LoadFailures(ctx context.Context, k TriageKey) (map[int64]Failure, error) {
	list, err := query(ctx, s.db, `SELECT `+failureSelect("")+` FROM triage_failures WHERE fingerprint = ? AND decider = ?`,
		[]any{k.Fingerprint, k.Decider}, scanFailure)
	out := make(map[int64]Failure, len(list))
	for _, f := range list {
		out[f.MessageID] = f
	}
	return out, err
}

// LoadScopeFailures returns the failures of a scope's eligible messages by
// row id. It never reads a failure outside the population, so the full plan
// can decide quarantine without loading another store's or role's failures.
func (s *Store) LoadScopeFailures(ctx context.Context, sc DecisionScope) (map[int64]Failure, error) {
	from, where, args, err := s.decisionScopeSQL(ctx, sc)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + failureSelect("tf.") + from + `
		JOIN triage_failures tf ON tf.message_id = m.id AND tf.fingerprint = ? AND tf.decider = ?
		WHERE ` + where + ` ORDER BY tf.message_id`
	qargs := append([]any{sc.Key.Fingerprint, sc.Key.Decider}, args...)
	list, err := query(ctx, s.db, q, qargs, scanFailure)
	if err != nil {
		return nil, fmt.Errorf("store: scope failures: %w", err)
	}
	out := make(map[int64]Failure, len(list))
	for _, f := range list {
		out[f.MessageID] = f
	}
	return out, nil
}

// TriageRun is one recorded triage run; Report is the run's JSON report.
type TriageRun struct {
	ID         int64          `json:"id"`
	StartedAt  string         `json:"started_at"`
	FinishedAt string         `json:"finished_at"`
	State      runstate.State `json:"state"`
	Key        TriageKey      `json:"key"`
	Report     string         `json:"report"`
	Error      string         `json:"error"`
}

// BeginTriageRun records a running triage run.
func (s *Store) BeginTriageRun(ctx context.Context, k TriageKey) (int64, error) {
	return s.beginRun(ctx, tableTriageRuns, "decider, fingerprint", "?, ?", k.Decider, k.Fingerprint)
}

// FinishTriageRun records the outcome of a run.
func (s *Store) FinishTriageRun(ctx context.Context, id int64, state runstate.State, report, errMsg string) error {
	return s.finishRun(ctx, tableTriageRuns, id, state, report, errMsg)
}

// ListTriageRuns returns the most recent runs, newest first.
func (s *Store) ListTriageRuns(ctx context.Context, limit int) ([]TriageRun, error) {
	return listRuns(ctx, s, "triage_runs", []string{"fingerprint", "decider", "report", "error"}, limit,
		func(r *sql.Rows, v *TriageRun) error {
			var st string
			err := r.Scan(&v.ID, &v.StartedAt, &v.FinishedAt, &st, &v.Key.Fingerprint, &v.Key.Decider, &v.Report, &v.Error)
			v.State = runstate.State(st)
			return err
		})
}
