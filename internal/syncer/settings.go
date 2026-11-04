package syncer

import "github.com/LLM-4-People/mailsense/internal/store"

// Settings the package owns. A key spelled at its read sites drifts: a typo
// silently reads the schema default instead of the configured value, so every
// read goes through one of these names and the schema check in
// settings_test.go fails when one stops resolving.
const (
	// SettingBatchSize is the number of messages written per store
	// transaction. The store owns the key; the syncer reads the same value.
	SettingBatchSize = store.SettingSyncBatchSize

	// SettingMaxLoggedSkips is how many item-level failures per folder are
	// logged individually; the rest are only counted.
	SettingMaxLoggedSkips = "sync.max_logged_skips"
	// SettingProgressInterval is the minimum time between two progress events
	// of one sync.
	SettingProgressInterval = "sync.progress_interval"
	// SettingSkipRatioFatal fails a folder whose skipped/seen ratio reaches
	// this value.
	SettingSkipRatioFatal = "sync.skip_ratio_fatal"
	// SettingDiscoveryInterval is how often a steady-state automatic sync
	// re-reads a source's folder list.
	SettingDiscoveryInterval = "sync.discovery.interval"

	// SettingDetailsEnabled turns the detail pass (completing partial
	// messages) on or off.
	SettingDetailsEnabled = "sync.details.enabled"
	// SettingDetailsBatch is how many partial messages the detail pass
	// completes per store transaction.
	SettingDetailsBatch = "sync.details.batch_size"
	// SettingDetailsMaxPerRun caps how many partial messages one run
	// completes; 0 means no cap.
	SettingDetailsMaxPerRun = "sync.details.max_per_run"
	// SettingDetailsSettleAfter leaves messages received more recently than
	// this to a later run, because the source may still be writing them.
	SettingDetailsSettleAfter = "sync.details.settle_after"

	// SettingRemovalHoldCount and SettingRemovalHoldRatio are the safety
	// valve that holds a removal instead of applying it.
	SettingRemovalHoldCount = "sync.removal.hold_count"
	SettingRemovalHoldRatio = "sync.removal.hold_ratio"
	// SettingRemovalAutoApplyAfter applies a held removal once the messages
	// stayed absent this long; 0 keeps held removals confirm-only.
	SettingRemovalAutoApplyAfter = "sync.removal.auto_apply_after"

	// SettingAuto and SettingAutoInterval drive the automatic sync.
	SettingAuto         = "sync.auto"
	SettingAutoInterval = "sync.auto.interval"

	// SettingContactsSyncEnabled and SettingCalendarSyncEnabled gate the
	// world pass's address-book and calendar collections.
	SettingContactsSyncEnabled = "contacts.sync.enabled"
	SettingCalendarSyncEnabled = "calendar.sync.enabled"
	// SettingCalendarPastDays and SettingCalendarFutureDays window the
	// calendar occurrences the world pass keeps.
	SettingCalendarPastDays   = "calendar.sync.past_days"
	SettingCalendarFutureDays = "calendar.sync.future_days"
)
