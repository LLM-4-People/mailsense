package index

// Settings the package owns. A key spelled at its read sites drifts: a typo
// silently reads the schema default instead of the configured value.
const (
	// SettingQuarantineAfter is how many failed attempts at one revision a
	// message is quarantined for.
	SettingQuarantineAfter = "index.quarantine_after"
	// SettingFolderRoles is the folder roles the index covers; it is part of
	// the coverage and retry fingerprint, so a change re-plans and re-arms.
	SettingFolderRoles = "index.folder_roles"
	// SettingRetryUnavailable is how long a run waits after the embedder was
	// unavailable before probing again.
	SettingRetryUnavailable = "index.retry_unavailable"
	// SettingMaxLoggedFailures is how many per-message failures a run logs
	// individually; the rest are only counted.
	SettingMaxLoggedFailures = "index.max_logged_failures"
	// SettingRunsKeep is how many run reports are retained.
	SettingRunsKeep = "index.runs.keep"
	// SettingAfterSync starts the index after a sync that advanced the mail.
	SettingAfterSync = "index.after_sync"
	// SettingEmbedProvider names the embedder record the index verifies
	// against and SettingEmbedBatch is how many messages one embedding call
	// carries.
	SettingEmbedProvider = "embed.provider"
	SettingEmbedBatch    = "embed.batch_size"
)
