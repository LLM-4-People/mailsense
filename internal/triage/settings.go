package triage

import "github.com/LLM-4-People/mailsense/internal/store"

// Settings the package owns. A key spelled at its read sites drifts: a typo
// silently reads the schema default instead of the configured value, so every
// read goes through one of these names and the schema check in
// settings_test.go fails when one stops resolving. The automatic settings are
// read from the live configuration on every event, so turning triage.auto off
// stops the next run without a restart.
const (
	SettingAuto     = "triage.auto"
	SettingBatch    = "triage.auto.batch"
	SettingInterval = "triage.auto.interval"
	// DeciderSetting names the provider/model/fingerprint the decider runs.
	DeciderSetting = "triage.decider"
	// SettingLeaseTTL is how long a run's lease is held before another
	// process may take the store over.
	SettingLeaseTTL = "triage.lease_ttl"
	// SettingFolderRoles is the setting that names the triaged population's
	// folder roles. It is the store's one spelling of the key, which the
	// triage reads, the layer-1 route key, the run fingerprint and the queue
	// inputs all use.
	SettingFolderRoles = store.SettingTriageFolderRoles
	// RulesFileSetting is the setting that names the owner's rules override.
	// It is the one spelling of the key: the loader reads it and every cache
	// stamp beside it (the triage version key, the layer-1 trigger) passes
	// the constant, so a rename cannot leave a cache keyed on a dead stamp.
	RulesFileSetting = "triage.rules.file"

	// SettingBudgetMaxCalls, SettingBudgetMaxUSD and
	// SettingBudgetAllowUnpriced bound one run's paid calls.
	SettingBudgetMaxCalls      = "triage.budget.max_calls"
	SettingBudgetMaxUSD        = "triage.budget.max_usd"
	SettingBudgetAllowUnpriced = "triage.budget.allow_unpriced"
	// SettingFactsEvidence joins the evidence facts to a state.
	SettingFactsEvidence = "triage.facts.evidence"
	// SettingWorkers is how many messages one run decides in parallel.
	SettingWorkers = "triage.workers"
	// SettingMaxLoggedFailures is how many per-message failures a run logs
	// individually; the rest are only counted.
	SettingMaxLoggedFailures = "triage.max_logged_failures"

	// SettingArchiveMaxLineBytes is the legacy export reader's line cap. It is
	// read here for the gold export; internal/source/archive reads the same
	// key and owns no exported name for it.
	SettingArchiveMaxLineBytes = "archive.max_line_bytes"
	// SettingWeakReplyWithin is the window a reply counts as a weak reply in
	// the feedback evaluation.
	SettingWeakReplyWithin = "feedback.weak_reply.within"
	// SettingHemisphere is the owner's hemisphere for the volume patterns.
	SettingHemisphere = "calendar.hemisphere"
)
