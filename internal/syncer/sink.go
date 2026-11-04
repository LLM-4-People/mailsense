package syncer

import (
	"context"
	"fmt"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// batchSink is the source.Sink of one folder sync: it validates, de-duplicates
// and batches messages into store transactions, counts skips, and throttles
// progress reports.
type batchSink struct {
	s        *Syncer
	clock    clock.Clock
	runID    int64
	sourceID string
	storeID  string
	folderID int64
	folder   string
	keys     map[string]bool // shared across the folders of one run
	buf      []mail.Message
	batch    int
	maxLog   int
	interval time.Duration
	totals   store.RunTotals
	position source.Progress
	lastEmit time.Time
	report   func(Progress)
	// Set by Snapshot: the size of the folder's snapshot and, when the
	// connector promised it was complete, the stored rows missing from it
	// and how many messages the folder held at that moment. Only these row
	// ids survive the folder sync: the full id set is released as soon as
	// the snapshot boundary has been reconciled, so a run retains O(absent)
	// instead of every folder's O(present) set (see Syncer.syncFolders).
	snapshotSize int
	complete     bool
	absent       []int64
	stored       int
}

var _ source.Sink = (*batchSink)(nil)

func (b *batchSink) Message(ctx context.Context, m mail.Message) error {
	b.totals.Seen++
	if m.SourceID != b.sourceID {
		b.skip(m.ID, fmt.Errorf("message claims source %q, connector is %q", m.SourceID, b.sourceID))
		return nil
	}
	if err := m.Validate(); err != nil {
		b.skip(m.ID, err)
		return nil
	}
	key := m.StoreID + "\x00" + m.ID
	if b.keys[key] {
		b.totals.Duplicates++
		b.s.log.Warn("duplicate message id in one run; first occurrence kept", "folder", b.folder, "id", m.ID)
		return nil
	}
	b.keys[key] = true
	b.alterations(&m)
	b.buf = append(b.buf, m)
	if len(b.buf) >= b.batch {
		return b.flush(ctx)
	}
	return nil
}

// alterations counts the connector's declared alterations of m (and logs
// the first ones of the folder, like skips).
func (b *batchSink) alterations(m *mail.Message) {
	for _, a := range m.Alterations {
		if b.totals.Altered == nil {
			b.totals.Altered = map[string]int{}
		}
		b.totals.Altered[a.Code]++
		if n := b.totals.Altered[a.Code]; n <= b.maxLog {
			args := append([]any{"folder", b.folder, "id", m.ID}, alterationAttrs(a)...)
			b.s.log.Info("message altered while read", args...)
		} else if n == b.maxLog+1 {
			b.s.log.Info("further alterations of this kind are only counted (sync.max_logged_skips)", "folder", b.folder, "code", a.Code)
		}
	}
}

// alterationAttrs is the log form of a declared alteration: its code class
// and the detail's length. The detail itself is never logged (it can name an
// address header); it stays stored on the message and is shown by the UI.
func alterationAttrs(a mail.Alteration) []any {
	return []any{"code", a.Code, "detail_bytes", len(a.Detail)}
}

func (b *batchSink) Skip(ref string, err error) {
	b.totals.Seen++
	b.skip(ref, err)
}

func (b *batchSink) skip(ref string, err error) {
	b.totals.Skipped++
	if b.totals.Skipped <= b.maxLog {
		b.s.log.Warn("item skipped", "folder", b.folder, "item", ref, "err", err)
	} else if b.totals.Skipped == b.maxLog+1 {
		b.s.log.Warn("further skips in this folder are only counted (sync.max_logged_skips)", "folder", b.folder)
	}
}

func (b *batchSink) Progress(p source.Progress) {
	b.position = p
	if b.clock.Now().Sub(b.lastEmit) >= b.interval {
		b.emit()
	}
}

// Snapshot compares the folder's current ids and stamps with the stored
// copies and returns the ids to deliver: new ones and ones whose stamp moved.
// When the connector promises the snapshot is complete, it also computes the
// stored rows missing from it here, while the presence set is still in hand:
// only the absent row ids and the stored count are kept for the end-of-run
// removal pass, never the presence set itself.
func (b *batchSink) Snapshot(ctx context.Context, items []source.Version, complete bool) (map[string]bool, error) {
	stored, err := b.s.store.FolderVersions(ctx, b.folderID)
	if err != nil {
		return nil, output(fmt.Errorf("syncer: read stored versions: %w", err))
	}
	want := map[string]bool{}
	present := make(map[string]bool, len(items))
	for _, it := range items {
		present[it.ID] = true
		if stamp, ok := stored[it.ID]; !ok || stamp != it.Stamp {
			want[it.ID] = true
		}
	}
	b.snapshotSize = len(items)
	b.complete, b.absent, b.stored = false, nil, 0
	if !complete {
		b.s.log.Info("folder changed while its snapshot was read; removals wait for the next run", "folder", b.folder)
		return want, nil
	}
	absent, held, err := b.s.store.AbsentMessages(ctx, b.folderID, present)
	if err != nil {
		return nil, output(fmt.Errorf("syncer: read absent messages: %w", err))
	}
	if absent == nil {
		absent = []int64{} // a complete snapshot always carries its (possibly empty) absence set
	}
	b.complete, b.absent, b.stored = true, absent, held
	return want, nil
}

// flush writes the buffered messages in one transaction. The store retries
// SQLITE_BUSY/LOCKED under its one busy policy; a write that still fails is
// an output failure and aborts the run.
func (b *batchSink) flush(ctx context.Context) error {
	if len(b.buf) == 0 {
		return nil
	}
	if err := b.s.store.UpsertMessages(ctx, b.runID, b.buf); err != nil {
		return output(fmt.Errorf("syncer: write failed (run aborted): %w", err))
	}
	b.totals.Written += len(b.buf)
	b.buf = b.buf[:0]
	b.emit()
	return nil
}

func (b *batchSink) emit() {
	b.lastEmit = b.clock.Now()
	b.s.progress(Progress{RunID: b.runID, SourceID: b.sourceID, Phase: source.PhaseMessages, StoreID: b.storeID, FolderID: b.folderID, Folder: b.folder, Totals: b.totals, Position: b.position}, b.report)
}
