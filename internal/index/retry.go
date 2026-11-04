package index

import (
	"time"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/events"
)

// retryKey identifies the exact index work a backoff belongs to. A change
// of the vector space, the cleaning rules, the indexed mail, the covered
// folder roles or the quarantine threshold makes the cached coverage and
// the wait stale: the next reconcile probes again.
type retryKey struct {
	space       string
	textVersion string
	mailGen     uint64
	roles       string
	quarantine  int
}

// retryState is the process-local wait after an unavailable stop.
type retryState struct {
	key      retryKey
	until    time.Time
	coverage Coverage
	// resetAt is the last time a change that can heal the embedder was
	// observed. It is kept across clears so a run that started before the
	// change does not arm a new wait when it finishes after it.
	resetAt time.Time
}

// retryKeyFor snapshots the inputs a backoff is valid for: the vector
// space and text version of the stopped run, the current mail generation
// (any mail write ends the wait), the covered folder roles and the
// quarantine threshold.
func (ix *Indexer) retryKeyFor(space, textVersion string) retryKey {
	s := ix.cfg.Snapshot()
	covered, _ := scopeOf(s)
	roles := rolesKey(covered)
	return retryKey{
		space:       space,
		textVersion: textVersion,
		mailGen:     ix.st.MailGeneration(),
		roles:       roles, // the same fingerprint the coverage key uses
		quarantine:  s.Int(SettingQuarantineAfter),
	}
}

// retryActive reports whether key is waiting and, if so, the coverage the
// stopped run started from. Reading it takes no plan and makes no provider
// call.
func (ix *Indexer) retryActive(key retryKey, now time.Time) (Coverage, bool) {
	ix.retryMu.Lock()
	defer ix.retryMu.Unlock()
	r := ix.retry
	if r.until.IsZero() || !now.Before(r.until) || r.key != key {
		return Coverage{}, false
	}
	return r.coverage, true
}

// armUnavailable arms the wait after a run that committed no vector stopped
// because the embedder could not serve. A zero or negative setting (not
// reachable through the schema, but possible for a programmatic config)
// disables the wait. A reset observed after the run started means the
// failure predates a change that may have healed the embedder, so no wait
// is armed.
func (ix *Indexer) armUnavailable(key retryKey, rep *Report, start time.Time) {
	delay := ix.cfg.Duration(SettingRetryUnavailable)
	ix.retryMu.Lock()
	clear := func() {
		ix.retry.key, ix.retry.until, ix.retry.coverage = retryKey{}, time.Time{}, Coverage{}
	}
	switch {
	case delay <= 0 || rep.Space == "":
		clear()
		ix.retryMu.Unlock()
		return
	case ix.retry.resetAt.After(start):
		clear()
		ix.retryMu.Unlock()
		ix.log.Debug("index unavailable retry not armed: a reset landed while the run was starting", "space", rep.Space)
		return
	}
	until := ix.clock.Now().Add(delay)
	ix.retry.key, ix.retry.until, ix.retry.coverage = key, until, rep.Before
	ix.retryMu.Unlock()
	ix.log.Info("index unavailable: the automatic reconcile waits", "space", rep.Space, "delay", delay, "until", until)
}

// clearRetry ends the wait but keeps the last reset observation.
func (ix *Indexer) clearRetry() {
	ix.retryMu.Lock()
	ix.retry.key, ix.retry.until, ix.retry.coverage = retryKey{}, time.Time{}, Coverage{}
	ix.retryMu.Unlock()
}

// ResetUnavailable ends the wait because something that can heal the
// embedder changed: a setting, a provider that reported healthy or a
// managed runtime that became ready. It is exported for the composition
// root, which keeps internal/index free of a sidecar import.
func (ix *Indexer) ResetUnavailable() {
	ix.retryMu.Lock()
	wasWaiting := !ix.retry.until.IsZero()
	ix.retry.resetAt = ix.clock.Now()
	ix.retry.key, ix.retry.until, ix.retry.coverage = retryKey{}, time.Time{}, Coverage{}
	ix.retryMu.Unlock()
	if wasWaiting {
		ix.log.Debug("index unavailable: the automatic reconcile may probe again")
	}
}

// AfterChanges ends the wait when the configuration changes (any effective
// change may fix a provider, a runtime or a scope) or when the configured
// embedding provider reports healthy again. stop ends both subscriptions.
func (ix *Indexer) AfterChanges(bus *events.Bus) (stop func(), err error) {
	buffer := ix.cfg.EventBuffer()
	stopConfig, err := events.On(bus, config.TopicChanged, buffer, func(config.Change) {
		ix.ResetUnavailable()
	})
	if err != nil {
		return nil, err
	}
	stopHealth, err := events.On(bus, engine.TopicProviderHealth, buffer, func(st engine.ProviderStatus) {
		if st.Healthy && st.Provider == ix.cfg.String("embed.provider") {
			ix.ResetUnavailable()
		}
	})
	if err != nil {
		stopConfig()
		return nil, err
	}
	return func() {
		stopHealth()
		stopConfig()
	}, nil
}
