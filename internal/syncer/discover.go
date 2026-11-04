package syncer

import (
	"context"
	"errors"
	"fmt"

	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// ErrMailboxUnreadable ends a run of a source whose picked mailboxes could
// not be read (offline, no rights): nothing was read, nothing recorded was
// replaced, and the mailboxes are waited on.
var ErrMailboxUnreadable = errors.New("syncer: the picked mailboxes of this source cannot be read now; they are waited on")

// Discover refreshes a source's mailbox list without reading any message
// (the picking screen's action): every store with its key, kind, counts
// and state, persisted and published on TopicMailboxes.
func (s *Syncer) Discover(ctx context.Context, c source.Connector) ([]store.MailboxView, error) {
	if !s.claim(c.ID()) {
		return nil, sourceBusyError(c.ID())
	}
	defer s.release(c.ID())
	if err := s.store.UpsertSource(ctx, c.ID(), c.Kind(), c.ID(), c.Capabilities()); err != nil {
		return nil, err
	}
	var rep Report
	_, err := s.discover(ctx, c, &rep)
	if err == nil {
		s.noteDiscovery(c.ID())
	}
	return rep.Mailboxes, err
}

// discoveryFresh reports whether the source's last successful folder
// discovery is still inside sync.discovery.interval.
func (s *Syncer) discoveryFresh(sourceID string) bool {
	ttl := s.cfg.Duration(SettingDiscoveryInterval)
	if ttl <= 0 {
		return false
	}
	s.mu.Lock()
	at, ok := s.discovered[sourceID]
	s.mu.Unlock()
	return ok && s.clock.Now().Sub(at) < ttl
}

// noteDiscovery records a successful folder discovery, so the next
// steady-state mail pass can reuse the stored folder list.
func (s *Syncer) noteDiscovery(sourceID string) {
	s.mu.Lock()
	s.discovered[sourceID] = s.clock.Now()
	s.mu.Unlock()
}

// invalidateDiscovery makes the next run rediscover, after a folder read
// failed: the stored list may no longer match the source.
func (s *Syncer) invalidateDiscovery(sourceID string) {
	s.mu.Lock()
	delete(s.discovered, sourceID)
	s.mu.Unlock()
}

// storedActiveFolders is the folder list of the last discovery, limited to
// the picked stores and the folders the source still syncs. It is the
// steady-state input between two discoveries; a source with no stored
// folder falls back to a real discovery.
func (s *Syncer) storedActiveFolders(ctx context.Context, c source.Connector) ([]mail.Folder, error) {
	infos, err := s.store.ListFolders(ctx, c.ID())
	if err != nil {
		return nil, output(err)
	}
	picked := source.Selected(s.cfg.Snapshot())
	out := make([]mail.Folder, 0, len(infos))
	for _, info := range infos {
		if !info.Active || !picked[source.StoreKey(c.ID(), info.Folder.StoreID)] {
			continue
		}
		out = append(out, info.Folder)
	}
	return out, nil
}

// folders chooses between a fresh discovery and the stored active folder
// list. A full pass (the first automatic pass after restart or a manual
// sync) always discovers. A steady-state mail pass reuses the stored list
// while sync.discovery.interval holds, so polling for new mail never pays a
// full folder discovery on every pass. The returned bool reports whether a
// discovery actually ran.
func (s *Syncer) folders(ctx context.Context, c source.Connector, world bool, rep *Report) ([]mail.Folder, bool, error) {
	if !world && s.discoveryFresh(c.ID()) {
		stored, err := s.storedActiveFolders(ctx, c)
		if err != nil {
			return nil, false, err
		}
		if len(stored) > 0 {
			s.log.Info("stored folders reused", "source", c.ID(), "folders", len(stored))
			return stored, false, nil
		}
	}
	picked, err := s.discover(ctx, c, rep)
	if err != nil {
		return nil, false, err
	}
	s.noteDiscovery(c.ID())
	return picked, true, nil
}

// discover lists the source's folders and mailboxes, records the mailbox
// list and each picked mailbox's reported owner identity, and returns the
// folders of the picked mailboxes only. A picked mailbox that could not be
// read, or whose identity was read only in part, is reported (log, event,
// report) and waited on: its recorded identity, and so its verdicts, stay.
func (s *Syncer) discover(ctx context.Context, c source.Connector, rep *Report) ([]mail.Folder, error) {
	all, err := c.Discover(ctx)
	if err != nil {
		return nil, fmt.Errorf("syncer: discover %s: %w", c.ID(), err)
	}
	picked := source.Selected(s.cfg.Snapshot())
	boxes := c.Mailboxes()
	if err := s.store.SaveMailboxes(ctx, c.ID(), boxes); err != nil {
		return nil, output(err)
	}
	for _, m := range boxes {
		if picked[m.Key] {
			if err := s.recordIdentity(ctx, m, rep); err != nil {
				return nil, output(err)
			}
		}
	}
	views, err := s.store.MailboxViews(ctx)
	if err != nil {
		return nil, output(err)
	}
	for _, v := range views {
		if v.SourceID == c.ID() {
			rep.Mailboxes = append(rep.Mailboxes, v)
		}
	}
	if s.bus != nil {
		events.Publish(s.bus, TopicMailboxes, views)
	}
	var out []mail.Folder
	for _, f := range all {
		if picked[source.StoreKey(c.ID(), f.StoreID)] {
			out = append(out, f)
		}
	}
	s.log.Info("mailboxes discovered", "source", c.ID(), "mailboxes", len(boxes), "picked_folders", len(out), "waiting", rep.Waiting)
	return out, nil
}

// recordIdentity saves the identity a picked mailbox's source reported,
// only from a complete read of an online store.
func (s *Syncer) recordIdentity(ctx context.Context, m source.Mailbox, rep *Report) error {
	switch {
	case !m.Online():
		rep.Waiting++
		s.log.Warn("picked mailbox cannot be read; waiting for it, its recorded identity and verdicts are kept",
			"key", m.Key, "kind", m.Kind, "error", m.Error)
		return nil
	case !m.IdentityComplete:
		rep.IdentityKept++
		s.log.Warn("owner identity of a picked mailbox read only in part; the recorded one is kept", "key", m.Key, "kind", m.Kind)
		return nil
	}
	return s.store.SaveReported(ctx, store.StoreRef{SourceID: m.SourceID, StoreID: m.StoreID, Name: m.Name, Kind: m.Kind}, m.Addresses)
}

// nothingRead explains a run that found no picked folder: no mailbox of
// the source picked (ErrNothingPicked), or the picked ones unreadable
// (ErrMailboxUnreadable); a picked, readable mailbox without mail folders
// is no error.
func (s *Syncer) nothingRead(sourceID string, rep *Report) error {
	picked := source.Selected(s.cfg.Snapshot())
	some := false
	for _, m := range rep.Mailboxes {
		some = some || picked[m.Key]
	}
	switch {
	case !some:
		return ErrNothingPicked
	case rep.Waiting > 0:
		return fmt.Errorf("%w: %s", ErrMailboxUnreadable, sourceID)
	}
	return nil
}
