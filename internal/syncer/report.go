// Package syncer runs a source.Connector into the store: per-folder
// cursors, batched writes, item-level failure counting with the skip-ratio
// rule, count reconciliation against the database, scope recompute, and
// progress events.
//
// Cursors are staged per folder and committed in one transaction only after
// the end-of-run removal pass succeeds. A run that fails or is
// cancelled before that pass removes nothing and commits no staged cursor;
// a removal failure leaves every previous cursor in place, so the next run
// re-evaluates the folders. Per store, removals and cursor commits are
// skipped while any discovered folder failed or delivered an incomplete
// snapshot: an id absent from one folder may have moved into the folder the
// run could not read. A message that moved between two complete snapshots is
// absent from both and has no destination row yet; removals hold such an
// unexplained absence one complete run before deleting it. A crash before the
// cursor transaction loses only the staged cursors, and the next run re-reads
// the folders and upserts idempotently; a crash after the cursor commit is
// safe because the removals already ran. A connector that never delivers a
// snapshot cannot be reconciled, so its folders keep their previous cursors
// and rows.
package syncer

import (
	"time"

	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// FolderReport is the outcome of one folder.
type FolderReport struct {
	Path      string          `json:"path"`
	StoreName string          `json:"store_name"`
	Role      mail.FolderRole `json:"role"`
	FolderID  int64           `json:"folder_id"`
	Totals    store.RunTotals `json:"totals"`
	Snapshot  int             `json:"snapshot"`  // ids listed by the connector's snapshot (0 = none)
	Removed   store.Removal   `json:"removed"`   // absent from a complete snapshot
	Unchanged bool            `json:"unchanged"` // cursor said nothing changed
	Error     string          `json:"error,omitempty"`
}

// StoreScope is the identity and the verdicts of one picked mailbox store.
type StoreScope struct {
	Store    store.StoreRef `json:"store"`
	Identity scope.Identity `json:"identity"`
	// Judged counts the messages (re)judged by this recompute (new or
	// changed mail, or every message after an identity change).
	Judged int                   `json:"judged"`
	Counts map[scope.Verdict]int `json:"counts"`
	// Blocked is the blocker code (BlockNoIdentity) of a store without an
	// owner identity: none of its mail is judged until the source reports
	// one or the user declares one.
	Blocked string `json:"blocked,omitempty"`
}

// ScopeReport is the outcome of a scope recompute: one entry per picked
// store, and the verdict counts over every stored message.
type ScopeReport struct {
	Stores []StoreScope          `json:"stores"`
	Counts map[scope.Verdict]int `json:"counts"`
}

// Judged is the total number of messages this recompute judged: new or
// changed mail, or every message after an identity change.
func (s *ScopeReport) Judged() int {
	n := 0
	for _, st := range s.Stores {
		n += st.Judged
	}
	return n
}

// DetailReport is the outcome of the detail pass (connectors whose Sync
// delivers partial messages).
type DetailReport struct {
	Completed int            `json:"completed"`
	Failed    int            `json:"failed"`
	Failures  map[string]int `json:"failures"` // by ItemError code
	Pending   int            `json:"pending"`  // still partial after the pass
	Capped    bool           `json:"capped"`   // sync.details.max_per_run left settled ones for the next run
	// Quarantined counts the source's messages set aside after their
	// detail read made the source fail (never read again automatically:
	// Syncer.RetryDetails, or a new revision of the message).
	Quarantined int  `json:"quarantined"`
	Disabled    bool `json:"disabled,omitempty"` // sync.details.enabled is off: no detail read
	// Altered counts the alterations the detail reads declared
	// (mail.Alteration codes: a header block cut, …), stored per message.
	Altered  map[string]int `json:"altered,omitempty"`
	Duration time.Duration  `json:"duration"`
}

// Report is the outcome of one run.
type Report struct {
	RunID    int64          `json:"run_id"`
	SourceID string         `json:"source_id"`
	Status   runstate.State `json:"status"`
	Error    string         `json:"error,omitempty"`
	// Partial is true when the run failed only at some folders: the mail
	// that arrived from the others is complete and stored. A cancelled run
	// and an output or unavailable-source failure are not partial. The run
	// stays failed and keeps its Error; Advanced is the one predicate that
	// says whether the stored mail changed.
	Partial bool            `json:"partial,omitempty"`
	Totals  store.RunTotals `json:"totals"`
	Folders []FolderReport  `json:"folders"`
	// Mailboxes lists the source's mailboxes after the discovery; a picked
	// one with an Error was not read (reported and waited on, never
	// replaced).
	Mailboxes []store.MailboxView `json:"mailboxes"`
	// Waiting counts the picked mailboxes that could not be read, and
	// IdentityKept those whose owner identity was read only in part: both
	// keep their recorded identity and verdicts.
	Waiting      int           `json:"waiting"`
	IdentityKept int           `json:"identity_kept"`
	Removed      store.Removal `json:"removed"`
	Details      *DetailReport `json:"details,omitempty"`
	World        *WorldReport  `json:"world,omitempty"`
	Scope        *ScopeReport  `json:"scope,omitempty"`
	Duration     time.Duration `json:"duration"`
}

// Advanced reports whether the run changed stored mail and is safe to
// follow: a completed run, or a partial run whose only failures were
// per-folder. Output/unavailable/cancel failures and no-op runs do not
// advance. This is the one trigger predicate; no consumer re-derives it.
func (r Report) Advanced() bool {
	if r.Status == runstate.Cancelled || (r.Status == runstate.Failed && !r.Partial) {
		return false
	}
	return r.Totals.Written > 0 || r.Removed.Deleted+r.Removed.Moved > 0 ||
		(r.Scope != nil && r.Scope.Judged() > 0) || (r.Details != nil && r.Details.Completed > 0)
}

// Progress is published while a run advances.
type Progress struct {
	RunID    int64           `json:"run_id"`
	SourceID string          `json:"source_id"`
	Phase    source.Phase    `json:"phase"` // source.PhaseMessages or source.PhaseDetails
	StoreID  string          `json:"store_id"`
	FolderID int64           `json:"folder_id"` // stored folder; 0 in the details phase
	Folder   string          `json:"folder"`    // display path; "" in the details phase
	Totals   store.RunTotals `json:"totals"`
	Position source.Progress `json:"position"`
}

// Topics published by the syncer.
var (
	TopicProgress = events.NewTopic[Progress]("sync.progress")
	TopicFinished = events.NewTopic[Report]("sync.finished")
	// TopicMailboxes carries the mailbox views (every source) after a
	// discovery or a change of the selection or the declared addresses.
	TopicMailboxes = events.NewTopic[[]store.MailboxView]("sync.mailboxes")
)
