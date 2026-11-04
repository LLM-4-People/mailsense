package evidence

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync/atomic"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// ErrBusy is returned while another evidence run is in progress.
var ErrBusy = errors.New("evidence: a run is already in progress")

// Deps are what the engine needs; Bus, Space and Clock may be nil.
type Deps struct {
	Config *config.Config
	Store  *store.Store
	Bus    *events.Bus
	// Space names the current vector space (the configured embedder's,
	// embed.Source); an error or nil leaves template_matches unavailable.
	Space func() (string, error)
	Clock clock.Clock
}

// Engine computes, stores and serves the structural evidence of every
// visible message (the registry of signals), and is the learning stage
// "evidence".
type Engine struct {
	cfg     *config.Config
	st      *store.Store
	bus     *events.Bus
	space   func() (string, error)
	clock   clock.Clock
	log     *slog.Logger
	signals []Signal
	bins    *Bins
	rules   *RuleSet
	judge   Judge
	single  jobs.Single  // one run at a time; a run wanted meanwhile follows it
	stage   *stage       // the learning stage (one: it keeps its status inputs)
	reasons *reasonBook  // the interned reasons of stored values
	writes  atomic.Int64 // batches that wrote evidence (Version)
}

// New builds the engine: the registry, the feature bins and the rules
// are checked (every problem reported).
func New(d Deps) (*Engine, error) {
	if d.Config == nil || d.Store == nil {
		return nil, errors.New("evidence: config and store are required")
	}
	signals := Signals()
	bins, err := LoadBins(d.Config, signals)
	if err != nil {
		return nil, err
	}
	rules, err := LoadRules(d.Config, signals)
	if err != nil {
		return nil, err
	}
	if err := Validate(signals); err != nil {
		return nil, err
	}
	e := &Engine{cfg: d.Config, st: d.Store, bus: d.Bus, space: d.Space, clock: clock.Or(d.Clock), log: logx.For("evidence"),
		signals: signals, bins: bins, rules: rules, judge: noJudge{}}
	e.stage, e.reasons = &stage{e: e}, newReasonBook(d.Store)
	return e, nil
}

// spaceName is the current vector space, "" when there is none.
func (e *Engine) spaceName() string {
	if e.space == nil {
		return ""
	}
	s, err := e.space()
	if err != nil {
		return ""
	}
	return s
}

// inputs is the fingerprint of everything a run reads: the visible mail
// (store.EvidenceInputs), the sources' capabilities, the schema (versions,
// the settings and pinned data the signals read) and the vector space.
func (e *Engine) inputs(ctx context.Context, s *Schema, space string) (string, error) {
	mail, err := e.st.EvidenceInputs(ctx)
	if err != nil {
		return "", err
	}
	caps, err := e.st.SourceCapabilities(ctx)
	if err != nil {
		return "", err
	}
	rawCaps, err := json.Marshal(caps) // a map: keys sorted
	if err != nil {
		return "", err
	}
	return digest.Hex([]byte(mail+"\n"+string(rawCaps)+"\n"+s.JSON+"\n"+space), 32), nil
}

// schema is the current schema.
func (e *Engine) schema() (*Schema, error) { return newSchema(e.signals, e.cfg.Snapshot()) }

// storedSchemas reads every recorded schema.
func (e *Engine) storedSchemas(ctx context.Context) (map[int64]stored, error) {
	raw, err := e.st.EvidenceSchemas(ctx)
	if err != nil {
		return nil, err
	}
	out := map[int64]stored{}
	for id, j := range raw {
		if out[id], err = parseStored(j); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Records returns the current evidence of the given messages (signals
// computed under another effective version are absent until the next run).
func (e *Engine) Records(ctx context.Context, ids []int64) (map[int64]Record, error) {
	s, err := e.schema()
	if err != nil {
		return nil, err
	}
	schemas, err := e.storedSchemas(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := e.st.EvidenceOf(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]Record, len(rows))
	for id, row := range rows {
		if out[id], err = decodeRow(s, schemas, row, e.reasonText(ctx)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// EachRecord streams the current evidence of every visible message.
func (e *Engine) EachRecord(ctx context.Context, fn func(id int64, r Record) error) error {
	s, err := e.schema()
	if err != nil {
		return err
	}
	schemas, err := e.storedSchemas(ctx)
	if err != nil {
		return err
	}
	return e.st.EachEvidence(ctx, func(row store.EvidenceRow) error {
		r, err := decodeRow(s, schemas, row, e.reasonText(ctx))
		if err != nil {
			return err
		}
		return fn(row.MessageID, r)
	})
}

// reasonText and reasonID read and intern the reasons of stored values.
func (e *Engine) reasonText(ctx context.Context) func(int64) (string, error) {
	return func(id int64) (string, error) { return e.reasons.text(ctx, id) }
}

func (e *Engine) reasonID(ctx context.Context) func(string) (int64, error) {
	return func(r string) (int64, error) { return e.reasons.id(ctx, r) }
}

// SignalList is the registry (the UI's list of signals and their docs).
func (e *Engine) SignalList() []Signal { return append([]Signal(nil), e.signals...) }
