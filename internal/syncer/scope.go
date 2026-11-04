package syncer

import (
	"context"
	"fmt"

	"github.com/LLM-4-People/mailsense/internal/scope"
	"github.com/LLM-4-People/mailsense/internal/source"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// RecomputeScope judges the mail of every picked store against that
// store's owner identity (store.Owners: the addresses its source reported
// at its last complete read plus those the user declares). Only messages that are new, changed, or judged
// against another identity are judged again. A store without an identity
// gets no verdicts (they are removed) and is reported as blocked: its
// owner is never guessed.
func (s *Syncer) RecomputeScope(ctx context.Context) (*ScopeReport, error) {
	refs, err := s.store.ScopeStores(ctx)
	if err != nil {
		return nil, fmt.Errorf("syncer: scope stores: %w", err)
	}
	owners, err := s.store.Owners(ctx)
	if err != nil {
		return nil, fmt.Errorf("syncer: identities: %w", err)
	}
	caps, err := s.store.SourceCapabilities(ctx)
	if err != nil {
		return nil, err
	}
	batch := s.cfg.Int(SettingBatchSize)
	rep := &ScopeReport{}
	for _, ref := range refs {
		ss := StoreScope{Store: ref, Identity: owners[ref.Key()].Identity}
		if !ss.Identity.Known() {
			ss.Blocked = BlockNoIdentity
			if _, err := s.store.ClearVerdicts(ctx, ref); err != nil {
				return nil, err
			}
			s.log.Warn("mailbox has no owner identity; its mail is not judged", "store", ref.Key(), "kind", ref.Kind)
		} else if ss.Judged, err = s.judge(ctx, ref, ss.Identity, caps[ref.SourceID], batch); err != nil {
			return nil, fmt.Errorf("syncer: scope of %s: %w", ref.Key(), err)
		}
		rep.Stores = append(rep.Stores, ss)
	}
	byCounts, err := s.store.VerdictCountsByStore(ctx)
	if err != nil {
		return nil, err
	}
	rep.Counts = make(map[scope.Verdict]int, len(byCounts))
	for _, counts := range byCounts {
		for verdict, n := range counts {
			rep.Counts[verdict] += n
		}
	}
	for i := range rep.Stores {
		st := &rep.Stores[i]
		st.Counts = byCounts[[2]string{st.Store.SourceID, st.Store.StoreID}]
		s.log.Info("store scope", "store", st.Store.Key(), "kind", st.Store.Kind, "address_count", len(st.Identity.Addresses),
			"judged", st.Judged, "owner_count", st.Counts[scope.Owner], "shared", st.Counts[scope.Shared],
			"unattributed", st.Counts[scope.Unattributed], "blocked", st.Blocked != "")
	}
	s.log.Info("scope recomputed", "stores", len(rep.Stores), "owner_count", rep.Counts[scope.Owner], "shared", rep.Counts[scope.Shared],
		"unattributed", rep.Counts[scope.Unattributed], "unjudged", rep.Counts[""])
	return rep, nil
}

// judge judges the store's stale evidence one bounded page at a time and
// writes each page before reading the next: the read cursor is closed when
// the callback runs, so the write never waits for the connection the scan
// holds. Evidence the source cannot report is not used (a value there is
// noise): the addressed-to-me flag only from sources with that capability.
func (s *Syncer) judge(ctx context.Context, ref store.StoreRef, id scope.Identity, caps source.Capabilities, batch int) (int, error) {
	dg := id.Digest()
	n := 0
	err := s.store.EachStaleScope(ctx, ref, dg, batch, func(rows []store.ScopeRow) error {
		pending := make([]store.VerdictRow, 0, len(rows))
		for _, r := range rows {
			if !caps.AddressedToMe {
				r.Evidence.AddressedToMe = ""
			}
			v, reason := scope.Decide(r.Evidence, id)
			pending = append(pending, store.VerdictRow{RowID: r.RowID, Revision: r.Revision, Verdict: v, Reason: reason})
		}
		n += len(pending)
		return s.store.WriteVerdicts(ctx, dg, pending)
	})
	return n, err
}
