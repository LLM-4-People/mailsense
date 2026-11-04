package syncer

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/learning"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// Blocker codes of the mailbox and sync stages.
const (
	BlockNoSelection = "no_mailbox_picked"
	BlockNotFound    = "mailbox_not_found"
	BlockUnreadable  = "mailbox_unreadable"
	BlockNoIdentity  = "no_owner_identity"
	// BlockNoOwnerReported: a source that never names a mailbox owner
	// (mail files, an export) — the owner must be declared.
	BlockNoOwnerReported = "owner_never_reported"
	// BlockDetailsQuarantined: messages whose detail read made the source
	// fail are set aside (learning.ActionRetryDetails).
	BlockDetailsQuarantined = "details_set_aside"
)

// MailboxesStage is the learning stage of the picked mailboxes: at least
// one picked, each seen by the last discovery, readable, and with an owner
// identity (reported by its source or declared).
func MailboxesStage(cfg *config.Config, st *store.Store) learning.Stage {
	return &mailboxesStage{cfg: cfg, st: st}
}

type mailboxesStage struct {
	cfg   *config.Config
	st    *store.Store
	cache learning.StageCache
}

func (*mailboxesStage) ID() string { return learning.StageMailboxes }

func (s *mailboxesStage) Status(ctx context.Context) (learning.StageStatus, error) {
	return s.cache.Status(stageCacheKey(s.cfg, s.st), func() (learning.StageStatus, error) {
		return s.status(ctx)
	})
}

// stageCacheKey fingerprints exactly what the mailbox and sync stages
// read: the picked stores, the declared owner addresses and the subaddress
// rule (the three settings behind MailboxViews and the scope owner), and
// the store's mail generation (mailboxes, folders, owner identities,
// selection, scope verdicts, mail and detail writes). Labels, routes,
// vectors and unrelated settings are deliberately absent: a label click, a
// route commit, an embedding write or a provider change never recomputes a
// mailbox aggregate.
//
// A sync cursor write (SetCursors) bumps no generation. It only turns an
// unsynced folder into a synced one, and a sync stage that is not done is
// Running — which the cache never stores — so a cached answer cannot hide
// a cursor that arrived after it. A done stage already has every cursor.
func stageCacheKey(cfg *config.Config, st *store.Store) string {
	snap := cfg.Snapshot()
	return fmt.Sprintf("%d|%s|%s|%s",
		st.MailGeneration(),
		source.Scope(snap),
		strings.Join(snap.Strings(source.SettingDeclared), "\x00"),
		strings.Join(snap.Strings(SettingSubaddressSeparators), "\x00"))
}

func (s *mailboxesStage) status(ctx context.Context) (learning.StageStatus, error) {
	picked := s.cfg.Strings(source.SettingSelected)
	out := learning.StageStatus{State: learning.Done, Progress: learning.Progress{Total: int64(len(picked)), Unit: "mailboxes"}}
	if len(picked) == 0 {
		out.State = learning.Blocked
		out.Blockers = []learning.Blocker{{Code: BlockNoSelection, Actions: []learning.Action{learning.Do(learning.ActionPickMailboxes)}}}
		return out, nil
	}
	views, err := s.st.MailboxViews(ctx)
	if err != nil {
		return out, err
	}
	caps, err := s.st.SourceCapabilities(ctx)
	if err != nil {
		return out, err
	}
	for _, v := range views {
		if !v.Selected {
			continue
		}
		if b := mailboxBlocker(v, caps[v.SourceID]); b != nil {
			out.Blockers = append(out.Blockers, *b)
			continue
		}
		out.Progress.Done++
	}
	if len(out.Blockers) > 0 {
		out.State = learning.Blocked
	}
	return out, nil
}

// mailboxBlocker is what keeps one picked mailbox from being learned. An
// unreadable store with a recorded identity is still waited on: its mail
// cannot be complete.
func mailboxBlocker(v store.MailboxView, caps source.Capabilities) *learning.Blocker {
	key := v.Key
	switch {
	case !v.Discovered:
		return &learning.Blocker{Code: BlockNotFound, Subject: key,
			Actions: []learning.Action{learning.Do(learning.ActionDiscover, "key", key), learning.Do(learning.ActionUnpickMailbox, "key", key)}}
	case !v.Online():
		return &learning.Blocker{Code: BlockUnreadable, Subject: key, Detail: v.Error,
			Actions: []learning.Action{learning.Do(learning.ActionReconnect, "key", key), learning.Do(learning.ActionUnpickMailbox, "key", key)}}
	case !v.Identity.Known():
		code := BlockNoIdentity
		if !caps.OwnerIdentity {
			code = BlockNoOwnerReported
		}
		return &learning.Blocker{Code: code, Subject: key, Actions: []learning.Action{learning.Do(learning.ActionDeclareAddress, "key", key)}}
	}
	return nil
}

// SyncStage is the learning stage of the picked mail itself: every folder
// of every picked store read completely, every message complete (details
// read) and judged (scope).
func SyncStage(cfg *config.Config, st *store.Store) learning.Stage {
	return &syncStage{cfg: cfg, st: st}
}

type syncStage struct {
	cfg   *config.Config
	st    *store.Store
	cache learning.StageCache
}

func (*syncStage) ID() string { return learning.StageSync }

func (s *syncStage) Status(ctx context.Context) (learning.StageStatus, error) {
	return s.cache.Status(stageCacheKey(s.cfg, s.st), func() (learning.StageStatus, error) {
		return s.status(ctx)
	})
}

func (s *syncStage) status(ctx context.Context) (learning.StageStatus, error) {
	picked := source.Selected(s.cfg.Snapshot())
	out := learning.StageStatus{State: learning.Done, Progress: learning.Progress{Unit: learning.UnitMessages}}
	counts, err := s.st.SyncProgress(ctx)
	if err != nil {
		return out, err
	}
	views, err := s.st.MailboxViews(ctx)
	if err != nil {
		return out, err
	}
	expected := map[string]int64{}
	for _, v := range views {
		expected[v.Key] = v.Items
	}
	synced, unsynced, partial, unjudged := 0, 0, 0, 0
	aside := map[string]int{} // set-aside messages by source
	for key := range picked {
		var c store.SyncCounts
		var src string
		for k, v := range counts {
			if source.StoreKey(k[0], k[1]) == key {
				c, src = v, k[0]
			}
		}
		if c.Folders == 0 {
			unsynced++
			out.Progress.Total += max(expected[key], 0)
			continue
		}
		synced++
		unsynced += c.Unsynced
		partial += c.Partial
		unjudged += c.Unjudged
		if c.SetAside > 0 {
			aside[src] += c.SetAside
		}
		out.Progress.Done += int64(c.Messages - max(c.Partial, c.Unjudged))
		out.Progress.Total += max(int64(c.Messages), expected[key])
	}
	for _, src := range slices.Sorted(maps.Keys(aside)) {
		out.Blockers = append(out.Blockers, learning.Blocker{Code: BlockDetailsQuarantined, Subject: src,
			Detail:  fmt.Sprintf("%d messages set aside: reading their details made the source fail", aside[src]),
			Actions: []learning.Action{learning.Do(learning.ActionRetryDetails, "source", src)}})
	}
	switch {
	case len(picked) == 0:
		out.State, out.Note = learning.Pending, "waiting for a picked mailbox"
	case unsynced > 0 || unjudged > 0:
		out.State = learning.Running
		out.Note = fmt.Sprintf("%d of %d picked stores synced; %d folders not read completely, %d messages waiting for details, %d not judged",
			synced, len(picked), unsynced, partial, unjudged)
	case partial > 0 || len(aside) > 0:
		// The mailbox is readable and judged; a handful of detail reads
		// (or set-aside messages) are warnings, not a reason to keep the
		// whole app locked. Their blockers stay visible with the retry
		// action, and the rest of the mail is usable.
		out.State = learning.Done
		out.Note = fmt.Sprintf("%d messages waiting for details, of which %d set aside", partial, sumValues(aside))
	}
	return out, nil
}

func sumValues(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}
