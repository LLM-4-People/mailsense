package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/LLM-4-People/mailsense/internal/runstate"
)

// Layer-1 model states.
const (
	Layer1Active    = "active"
	Layer1Candidate = "candidate"
	Layer1Retired   = "retired"
)

// Layer1Model is one stored layer-1 head.
type Layer1Model struct {
	ID         int64  `json:"id"`
	Question   string `json:"question"`
	VersionKey string `json:"version_key"`
	Kind       string `json:"kind"`
	Classes    int    `json:"classes"`
	Params     []byte `json:"-"`
	Meta       string `json:"meta"`
	Metrics    string `json:"metrics"`
	State      string `json:"state"`
	RunID      int64  `json:"run_id"`
	CreatedAt  string `json:"created_at"`
}

// SaveLayer1Candidate stores a trained head as a candidate (a head of the
// same question and version is replaced, unless it is the active one:
// then the active row is returned unchanged).
func (s *Store) SaveLayer1Candidate(ctx context.Context, m Layer1Model) (int64, error) {
	var id int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var state string
		err := tx.QueryRowContext(ctx, "SELECT id, state FROM layer1_models WHERE question = ? AND version_key = ?", m.Question, m.VersionKey).Scan(&id, &state)
		switch {
		case err == nil && state == Layer1Active:
			return nil
		case err == nil:
			_, err = tx.ExecContext(ctx, `UPDATE layer1_models SET kind = ?, classes = ?, params = ?, meta = ?, metrics = ?, state = ?,
				run_id = ?, created_at = ? WHERE id = ?`, m.Kind, m.Classes, m.Params, m.Meta, m.Metrics, Layer1Candidate, m.RunID, s.now(), id)
			return err
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO layer1_models (question, version_key, kind, classes, params, meta, metrics, state, run_id, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, m.Question, m.VersionKey, m.Kind, m.Classes, m.Params, m.Meta, m.Metrics, Layer1Candidate, m.RunID, s.now())
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("store: save layer-1 model of %s: %w", m.Question, err)
	}
	return id, nil
}

// PromoteLayer1 makes a stored head its question's active one (the
// previous active head is retired), in one transaction.
func (s *Store) PromoteLayer1(ctx context.Context, id int64) error {
	return s.inTx(ctx, func(tx *sql.Tx) error { return promoteLayer1(ctx, tx, id) })
}

func promoteLayer1(ctx context.Context, tx *sql.Tx, id int64) error {
	var q string
	if err := tx.QueryRowContext(ctx, "SELECT question FROM layer1_models WHERE id = ?", id).Scan(&q); err != nil {
		return fmt.Errorf("store: promote layer-1 model %d: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE layer1_models SET state = ? WHERE question = ? AND state = ? AND id <> ?", Layer1Retired, q, Layer1Active, id); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "UPDATE layer1_models SET state = ? WHERE id = ?", Layer1Active, id)
	return err
}

// ActiveLayer1 returns the active head of every question.
func (s *Store) ActiveLayer1(ctx context.Context) ([]Layer1Model, error) {
	return s.layer1Where(ctx, "state = ?", Layer1Active)
}

// Layer1Models returns every stored head of a question, newest first.
func (s *Store) Layer1Models(ctx context.Context, question string) ([]Layer1Model, error) {
	return s.layer1Where(ctx, "question = ?", question)
}

func (s *Store) layer1Where(ctx context.Context, where string, arg any) ([]Layer1Model, error) {
	return query(ctx, s.db, `SELECT id, question, version_key, kind, classes, params, meta, metrics, state, run_id, created_at
		FROM layer1_models WHERE `+where+` ORDER BY id DESC`, []any{arg}, func(r *sql.Rows, m *Layer1Model) error {
		return r.Scan(&m.ID, &m.Question, &m.VersionKey, &m.Kind, &m.Classes, &m.Params, &m.Meta, &m.Metrics, &m.State, &m.RunID, &m.CreatedAt)
	})
}

// PruneLayer1 keeps, per question, the active head and the newest keep
// others; it returns how many it removed.
func (s *Store) PruneLayer1(ctx context.Context, keep int) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM layer1_models WHERE state <> ? AND id NOT IN (
		SELECT id FROM (SELECT id, ROW_NUMBER() OVER (PARTITION BY question ORDER BY id DESC) AS n FROM layer1_models WHERE state <> ?) WHERE n <= ?)`,
		Layer1Active, Layer1Active, max(keep, 0))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Layer1Run is one recorded layer-1 run.
type Layer1Run struct {
	ID         int64          `json:"id"`
	StartedAt  string         `json:"started_at"`
	FinishedAt string         `json:"finished_at"`
	State      runstate.State `json:"state"`
	Inputs     string         `json:"inputs"`
	Report     string         `json:"report"`
	Error      string         `json:"error"`
}

// BeginLayer1Run records a running run over the inputs' fingerprint.
func (s *Store) BeginLayer1Run(ctx context.Context, inputs string) (int64, error) {
	return s.beginRun(ctx, tableLayer1Runs, "inputs", "?", inputs)
}

// FinishLayer1Run records a run's outcome.
func (s *Store) FinishLayer1Run(ctx context.Context, id int64, state runstate.State, report, errMsg string) error {
	return s.finishRun(ctx, tableLayer1Runs, id, state, report, errMsg)
}

// ListLayer1Runs returns the most recent runs, newest first.
func (s *Store) ListLayer1Runs(ctx context.Context, limit int) ([]Layer1Run, error) {
	return listRuns(ctx, s, "layer1_runs", []string{"inputs", "report", "error"}, limit,
		func(r *sql.Rows, v *Layer1Run) error {
			var st string
			err := r.Scan(&v.ID, &v.StartedAt, &v.FinishedAt, &st, &v.Inputs, &v.Report, &v.Error)
			v.State = runstate.State(st)
			return err
		})
}

// Layer1Commit is the outcome of a layer-1 run or re-score, written
// atomically (a failure or a cancel changes nothing): the candidates
// promoted, the questions whose active model retires without a successor,
// the active models' updated meta (re-scored thresholds), the answers
// (replacing the key's as a whole, streamed: Decisions calls fn for each,
// never holding them all), the route answers (replacing the profile's as a
// whole and pruning every other profile, streamed: Routes calls fn for each;
// nil leaves the stored routes untouched), the route preview answers (the
// same replacement and pruning for the separate preview table, streamed:
// Preview calls fn for each; nil leaves the stored previews untouched) and
// the run's outcome (Report is called once the answers are written: it may
// count them).
//
// The production one-pass Answers form is staged: each message's decision
// (the expensive inference output) is streamed into a connection-private
// TEMP table before the main transaction starts, then replayed inside one
// short BEGIN IMMEDIATE that also promotes, retires, derives the route and
// preview answers from the decision, writes the report and marks the run
// succeeded. Answer computation is therefore outside the main database's
// write lock, and the disk carries one staged copy instead of three. The
// legacy separate streams (Answers nil) have no inference phase to stage and
// stay inside the transaction, exactly as before.
type Layer1Commit struct {
	RunID   int64
	Report  func() (string, error)
	Promote []int64
	Retire  []string
	Meta    map[int64]string
	Key     TriageKey
	// RouteKey is the identity of every configuration input that can change
	// a route or preview answer. Route rows are written under it, while
	// decisions stay under Key: a route configuration change must not
	// invalidate (or re-bill) paid decisions, and a paid decider change must
	// not silently serve route answers computed under another configuration.
	RouteKey  string
	Decisions func(fn func(Decision) error) error
	Routes    func(fn func(RouteAnswer) error) error
	Preview   func(fn func(PreviewAnswer) error) error
	// Answers, when set, is the one-pass form of the three streams above:
	// one traversal that writes a message's decision, route answer and
	// preview together. The older fields stay for a re-score and for tests
	// that exercise one table at a time.
	Answers *Layer1Answers
}

// layer1CommitTxSpan, when non-nil, receives the wall-clock span of the short
// layer-1 commit transaction. Tests use it to pin that answer computation is
// outside the write lock; production leaves it nil.
var layer1CommitTxSpan func(time.Duration)

// CommitLayer1 applies a commit atomically and marks its run succeeded.
func (s *Store) CommitLayer1(ctx context.Context, c Layer1Commit) error {
	if err := s.commitLayer1(ctx, c); err != nil {
		return fmt.Errorf("store: commit layer-1 run %d: %w", c.RunID, err)
	}
	// The route rows are visible now: the next view of them must not reuse
	// a snapshot built from the previous commit. A commit that writes no
	// route answer (decisions only, or nothing) changes no such view. The
	// one-pass Answers form is the production path, so it must bump the
	// generation exactly like the separate streams.
	if c.Routes != nil || c.Preview != nil ||
		(c.Answers != nil && (c.Answers.Routes || c.Answers.Preview)) {
		s.touchRoutes()
	}
	// The commit is the largest writer in the app. A WAL over the
	// configured ceiling is folded back now, while the writer slot is
	// ours; a reader that still needs an old snapshot leaves it partial
	// and the maintenance pass retries. A checkpoint failure never fails
	// the commit: the data is already durable.
	if err := s.checkpointIfLarge(ctx); err != nil {
		s.log.Warn("post-commit checkpoint failed", "err", err, "wal_bytes", s.WALSize())
	}
	return nil
}

// commitLayer1 stages a one-pass commit's answers on one dedicated
// connection, then runs the short transaction on that same connection. The
// staging phase takes no main-database lock; the transaction promotes,
// retires, replays the staged rows and writes the report as one unit. The
// legacy separate streams skip staging and keep their single transaction.
func (s *Store) commitLayer1(ctx context.Context, c Layer1Commit) error {
	if c.Answers == nil {
		return s.inTx(ctx, func(tx *sql.Tx) error { return s.commitLayer1Tx(ctx, tx, c, nil) })
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	// The conn is held for the whole commit: staging and the short
	// transaction share it, and no second connection is used for the write
	// phase.
	defer func() { _ = conn.Close() }()
	stage, err := newLayer1Stage(ctx, conn, c.Answers.Derive != nil)
	if err != nil {
		return err
	}
	defer func() {
		// The transaction is over; a cancelled caller context must not leave
		// the staging table on a pooled connection.
		dropCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := stage.drop(dropCtx); err != nil {
			s.log.Warn("layer-1 staging table not dropped", "err", err)
		}
	}()
	if err := stage.fill(ctx, c.Answers.Each); err != nil {
		return err
	}
	if layer1StagePayload != nil {
		payload, err := stage.payload(ctx)
		if err != nil {
			return err
		}
		layer1StagePayload(payload)
	}
	started := time.Now()
	err = inTxOn(ctx, conn, func(tx *sql.Tx) error { return s.commitLayer1Tx(ctx, tx, c, stage) })
	if layer1CommitTxSpan != nil {
		layer1CommitTxSpan(time.Since(started))
	}
	return err
}

// commitLayer1Tx is the short write transaction of a commit. staged is the
// answer population when c.Answers is set; promotion, retirement, meta,
// answers, the report and the run's success are one atomic unit.
func (s *Store) commitLayer1Tx(ctx context.Context, tx *sql.Tx, c Layer1Commit, staged *layer1Stage) error {
	for _, id := range c.Promote {
		if err := promoteLayer1(ctx, tx, id); err != nil {
			return err
		}
	}
	for _, q := range c.Retire {
		if _, err := tx.ExecContext(ctx, "UPDATE layer1_models SET state = ? WHERE question = ? AND state = ?", Layer1Retired, q, Layer1Active); err != nil {
			return err
		}
	}
	for id, meta := range c.Meta {
		if _, err := tx.ExecContext(ctx, "UPDATE layer1_models SET meta = ? WHERE id = ?", meta, id); err != nil {
			return err
		}
	}
	if c.Answers != nil {
		if staged == nil {
			return errors.New("store: a staged layer-1 commit needs its staged answers")
		}
		a := *c.Answers
		a.Each = func(fn func(Layer1Answer) error) error { return staged.each(ctx, tx, a.Derive, fn) }
		if err := s.streamLayer1Answers(ctx, tx, c.Key, c.RouteKey, a); err != nil {
			return err
		}
		if c.Answers.Routes {
			if _, err := pruneRouteAnswers(ctx, tx, c.RouteKey); err != nil {
				return err
			}
		}
		if c.Answers.Preview {
			if _, err := prunePreviewAnswers(ctx, tx, c.RouteKey); err != nil {
				return err
			}
		} else if _, err := clearPreviewAnswers(ctx, tx); err != nil {
			// Test mode is off: every stored preview is unreachable.
			return err
		}
	} else {
		// A nil stream leaves its table untouched, exactly like the
		// route and preview streams; calling a nil function panicked.
		if c.Decisions != nil {
			if err := s.streamDecisions(ctx, tx, c.Key, true, c.Decisions); err != nil {
				return err
			}
		}
		if c.Routes != nil {
			if c.RouteKey == "" {
				return errors.New("store: a route commit needs its route key")
			}
			if err := s.streamRouteAnswers(ctx, tx, c.RouteKey, c.Routes); err != nil {
				return err
			}
			if _, err := pruneRouteAnswers(ctx, tx, c.RouteKey); err != nil {
				return err
			}
		}
		if c.Preview != nil {
			if c.RouteKey == "" {
				return errors.New("store: a preview commit needs its route key")
			}
			if err := s.streamPreviewAnswers(ctx, tx, c.RouteKey, c.Preview); err != nil {
				return err
			}
			if _, err := prunePreviewAnswers(ctx, tx, c.RouteKey); err != nil {
				return err
			}
		}
	}
	report, err := c.Report()
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE layer1_runs SET finished_at = ?, status = ?, report = ?, error = '' WHERE id = ?",
		s.now(), string(runstate.Succeeded), report, c.RunID); err != nil {
		return err
	}
	// Only the newest run's label digests are read (the retrain decision
	// compares the last run with the current labels). Every older retained
	// run kept a full mailbox-sized array, so strip theirs in the same
	// transaction: the run row stays, its report stops carrying the copy.
	_, err = tx.ExecContext(ctx, `UPDATE layer1_runs SET report = json_remove(report, '$.labels')
		WHERE id <> ? AND status = ? AND report LIKE '%"labels"%'`, c.RunID, string(runstate.Succeeded))
	return err
}
