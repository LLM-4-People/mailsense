package syncer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// Syncer orchestrates connector runs into the store.
type Syncer struct {
	store      *store.Store
	cfg        *config.Config
	bus        *events.Bus
	clock      clock.Clock
	log        *slog.Logger
	mu         sync.Mutex
	running    map[string]bool // source ids with a run in progress
	status     map[string]SourceStatus
	discovered map[string]time.Time // source id -> last successful folder discovery
	// backfill is one HTML backfill job at a time (any source): two would
	// read the same items twice and compete for the source's COM thread.
	backfill jobs.Single
}

// ErrSourceBusy is returned when a source is already being synced: two
// interleaved runs of one source would corrupt each other's reconciliation.
var ErrSourceBusy = errors.New("syncer: this source is already syncing")

// sourceBusyError is the one wrap of ErrSourceBusy: the sentinel says what
// happened, the source id says where. Starting a sync job, running one and
// rediscovering a source's mailboxes all refuse a second concurrent run of the
// same source, and errors.Is must keep working from every entry point, so the
// message is built here once instead of at each refusal.
func sourceBusyError(id string) error { return fmt.Errorf("%w: %s", ErrSourceBusy, id) }

// ErrNothingPicked ends a run of a source none of whose mailboxes is
// picked (source.SettingSelected): its mailbox list is refreshed and its
// stored folders become inactive, but no message is read.
var ErrNothingPicked = errors.New("syncer: no mailbox of this source is picked; pick one to sync it")

// New returns a syncer; bus and clk may be nil (no events; wall clock).
func New(st *store.Store, cfg *config.Config, bus *events.Bus, clk clock.Clock) *Syncer {
	return &Syncer{store: st, cfg: cfg, bus: bus, clock: clock.Or(clk), log: logx.For("syncer"),
		running: map[string]bool{}, status: map[string]SourceStatus{}, discovered: map[string]time.Time{}}
}

// claim marks a source as syncing; release undoes it.
func (s *Syncer) claim(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[id] {
		return false
	}
	s.running[id] = true
	return true
}

func (s *Syncer) release(id string) {
	s.mu.Lock()
	delete(s.running, id)
	s.mu.Unlock()
}

// JobKind is the jobs kind of a sync run.
const JobKind = "sync"

// Start runs a full Run as a background job. The source is claimed before the
// job exists, so two starts cannot create two jobs that then race for the
// same source (the second call gets ErrSourceBusy).
func (s *Syncer) Start(m *jobs.Manager, c source.Connector) (*jobs.Job, error) {
	return s.start(m, c, true)
}

// StartMail runs the mail pass as a background job, without the calendar and
// address-book collections. It is the automatic path's steady state: new mail
// must be fetched often, while the world model changes slowly and is refreshed
// by the first pass or a manual sync.
func (s *Syncer) StartMail(m *jobs.Manager, c source.Connector) (*jobs.Job, error) {
	return s.start(m, c, false)
}

func (s *Syncer) start(m *jobs.Manager, c source.Connector, world bool) (*jobs.Job, error) {
	if !s.claim(c.ID()) {
		return nil, sourceBusyError(c.ID())
	}
	j, err := m.StartEssential(JobKind, c.ID(), func(ctx context.Context, report func(jobs.Progress)) error {
		defer s.release(c.ID())
		_, err := s.runFolders(ctx, c, nil, world, func(p Progress) {
			msg := fmt.Sprintf("%s: %d written, %d skipped", p.Folder, p.Totals.Written, p.Totals.Skipped)
			if p.Phase == source.PhaseDetails {
				msg = fmt.Sprintf("details: %d of %d", p.Position.Done, p.Position.Total)
			}
			report(jobs.Progress{Done: p.Position.Done, Total: p.Position.Total, Unit: p.Position.Unit, Message: msg})
		})
		return err
	})
	if err != nil {
		s.release(c.ID())
	}
	return j, err
}

// Run syncs every discovered folder of c, completes partial messages when
// c is a source.DetailReader, then recomputes scope.
// A second Run of a source that is already syncing fails with ErrSourceBusy.
func (s *Syncer) Run(ctx context.Context, c source.Connector, report func(Progress)) (Report, error) {
	return s.runClaimed(ctx, c, nil, true, report)
}

// RunFolders is Run limited to the discovered folders keep accepts (nil:
// all). Removals and the vanished-folder check only run for a full run, so
// a partial run never deactivates the folders it skipped.
func (s *Syncer) RunFolders(ctx context.Context, c source.Connector, keep func(mail.Folder) bool, report func(Progress)) (Report, error) {
	return s.runClaimed(ctx, c, keep, true, report)
}

func (s *Syncer) runClaimed(ctx context.Context, c source.Connector, keep func(mail.Folder) bool, world bool, report func(Progress)) (Report, error) {
	if !s.claim(c.ID()) {
		return Report{SourceID: c.ID(), Status: runstate.Failed}, sourceBusyError(c.ID())
	}
	defer s.release(c.ID())
	return s.runFolders(ctx, c, keep, world, report)
}

// runFolders is the one run body; its caller owns the source claim. world
// gates the calendar and address-book collections, never the mail pass.
func (s *Syncer) runFolders(ctx context.Context, c source.Connector, keep func(mail.Folder) bool, world bool, report func(Progress)) (rep Report, runErr error) {
	rep = Report{SourceID: c.ID(), Status: runstate.Failed}
	s.noteAttempt(c.ID())
	defer func() {
		if rep.Status == "" {
			rep.Status = runstate.Of(ctx, runErr)
		}
		if rep.Error == "" && runErr != nil {
			rep.Error = runErr.Error()
		}
		s.noteFinish(c.ID(), rep)
	}()
	start := s.clock.Now()
	if err := s.store.UpsertSource(ctx, c.ID(), c.Kind(), c.ID(), c.Capabilities()); err != nil {
		return rep, err
	}
	runID, err := s.store.BeginRun(ctx, c.ID())
	if err != nil {
		return rep, err
	}
	rep.RunID = runID
	s.log.Info("sync started", "run", runID, "source", c.ID(), "kind", c.Kind())
	picked, fresh, err := s.folders(ctx, c, world, &rep)
	if err != nil {
		runErr = err
	} else if runErr = s.syncFolders(ctx, runID, c, picked, keep, &rep, report); runErr != nil && !fresh {
		// A stored folder list that no longer matches the source shows up as
		// a failed folder read; rediscover on the next pass instead of
		// retrying the same stale path.
		s.invalidateDiscovery(c.ID())
	}
	if world && len(picked) > 0 && ctx.Err() == nil && !aborts(runErr) {
		// The world model follows the mail pass: a calendar or address-book
		// failure is reported per collection and never invalidates mail.
		world := s.syncWorld(ctx, c, &rep)
		rep.World = &world
	}
	if len(picked) == 0 && runErr == nil {
		runErr = s.nothingRead(c.ID(), &rep)
	}
	if dr, ok := c.(source.DetailReader); ok && len(picked) > 0 && ctx.Err() == nil && !aborts(runErr) {
		det, err := s.syncDetails(ctx, runID, c, dr, report)
		rep.Details = det
		runErr = errors.Join(runErr, err)
	}
	if ctx.Err() == nil && !isOutput(runErr) {
		sc, err := s.RecomputeScope(ctx)
		if err != nil {
			runErr = errors.Join(runErr, err)
		}
		rep.Scope = sc
	}
	rep.Status = runstate.Of(ctx, runErr)
	// A folder error is partial success: the other folders' mail is complete.
	// A cancelled run and an output or unavailable-source failure are not.
	rep.Partial = rep.Status == runstate.Failed && !aborts(runErr) && !isOutput(runErr)
	if runErr != nil {
		rep.Error = runErr.Error()
	}
	rep.Duration = s.clock.Now().Sub(start)
	// The run record is written even when the run's own context is gone.
	if err := s.store.FinishRun(context.WithoutCancel(ctx), runID, rep.Status, rep.Totals, runErr); err != nil {
		runErr = errors.Join(runErr, err)
	}
	s.log.Info("sync finished", "run", runID, "status", rep.Status, "seen", rep.Totals.Seen, "written", rep.Totals.Written,
		"skipped", rep.Totals.Skipped, "duplicates", rep.Totals.Duplicates, "duration", rep.Duration, "err", rep.Error)
	if s.bus != nil {
		events.Publish(s.bus, TopicFinished, rep)
	}
	return rep, runErr
}

// syncFolders runs every discovered folder. A folder that fails (connector
// error, failed checks) is reported and the run goes on with the others; an
// output failure (the store cannot write), an unavailable source
// (source.ErrUnavailable: its application is gone or recovering — the
// next folder would only hammer it) or a cancelled context aborts the
// run. Removals are applied after every folder ran (removal.go), so a
// message moved between two folders is recognised as a move; one that moved
// between the two snapshots and is absent from both is held one complete run
// instead of being deleted. A complete snapshot's absence candidates are
// computed when the snapshot is taken (sink.Snapshot), so only their row ids
// and the stored count are retained until this pass, never the presence set.
//
// A folder's next cursor is only staged here. Per store, the view is
// complete only when every discovered folder either delivered a complete
// snapshot or was verified unchanged; a failed or incomplete folder makes
// the store's view partial. Partial stores skip their removals and commit
// none of their cursors, so the next run re-reads them; other stores are
// unaffected. Every staged cursor is committed in one transaction after the
// removal pass succeeds.
func (s *Syncer) syncFolders(ctx context.Context, runID int64, c source.Connector, all []mail.Folder, keep func(mail.Folder) bool, rep *Report, report func(Progress)) error {
	folders := all
	if keep != nil {
		folders = nil
		for _, f := range all {
			if keep(f) {
				folders = append(folders, f)
			}
		}
	}
	// keys de-duplicates a message id across the folders of one run. The
	// store's unique (source, store, msg_id) key would otherwise update the
	// row into the later folder, hiding the duplicate from the report and
	// leaving the earlier folder's written count unreconcilable. Duplicates
	// are inherently cross-folder, so the set cannot be reset per folder; it
	// is bounded by the ids delivered this run, not by the stored rows of the
	// folders.
	keys := map[string]bool{}
	var removals []removal
	var errs []error
	staged := map[string]map[int64]string{}
	total := map[string]int{}
	for _, f := range all {
		total[f.StoreID]++
	}
	completeCount := map[string]int{}
	for _, f := range folders {
		res, err := s.syncFolder(ctx, runID, c, f, keys, report)
		fr := res.report
		rep.Folders = append(rep.Folders, fr)
		rep.Totals = rep.Totals.Add(fr.Totals)
		if err != nil {
			if ctx.Err() != nil || aborts(err) {
				if errors.Is(err, source.ErrUnavailable) {
					s.log.Warn("source unavailable; the run ends here", "source", c.ID(), "folder", f.Path, "err", err)
				}
				return fmt.Errorf("folder %s: %w", f.Path, err)
			}
			errs = append(errs, fmt.Errorf("folder %s: %w", f.Path, err))
			continue
		}
		if !res.complete && !fr.Unchanged {
			continue // an incomplete view must not remove or advance anything
		}
		completeCount[f.StoreID]++
		if res.complete && res.next != "" && !fr.Unchanged {
			if staged[f.StoreID] == nil {
				staged[f.StoreID] = map[int64]string{}
			}
			staged[f.StoreID][fr.FolderID] = string(res.next)
		}
		if res.complete {
			r := res.removal
			r.index = len(rep.Folders) - 1
			removals = append(removals, r)
		}
	}
	complete := make(map[string]bool, len(total))
	for store, n := range total {
		complete[store] = completeCount[store] == n
	}
	if err := s.applyRemovals(ctx, c, all, removals, complete, keep == nil, rep); err != nil {
		return err
	}
	cursors := map[int64]string{}
	for store, next := range staged {
		if !complete[store] {
			continue
		}
		for folderID, cursor := range next {
			cursors[folderID] = cursor
		}
	}
	if len(cursors) > 0 {
		if err := s.store.SetCursors(ctx, cursors); err != nil {
			return output(err)
		}
	}
	return errors.Join(errs...)
}
