package mail

// Setting keys owned by the mail domain. Threading, evidence, the installer's
// learner and the store's company book all read the same subject and shared
// domain settings; a second spelling in any of them was a drift waiting to
// happen. A key spelled at its read site drifts: a typo silently reads the
// schema default instead of the configured value, so every read goes through
// one of these names and the schema check in settings_test.go fails when one
// stops resolving.
const (
	SettingSubjectPrefixes        = "mail.subject.prefixes"
	SettingSubjectPrefixesLearned = "mail.subject.prefixes_learned"
	SettingSharedDomains          = "mail.domains.shared"

	// SettingNoDateYearAtMost and SettingNoDateYearAtLeast bracket the
	// far-past and far-future years a connector uses as a "no date" sentinel.
	SettingNoDateYearAtMost  = "mail.no_date.year_at_most"
	SettingNoDateYearAtLeast = "mail.no_date.year_at_least"

	// SettingBodyMaxChars, SettingHTMLMaxChars and SettingHeadersMaxChars cap
	// the text a connector keeps from one message; every connector reads the
	// same caps through LimitsFromConfig.
	SettingBodyMaxChars    = "mail.body.max_chars"
	SettingHTMLMaxChars    = "mail.body.html_max_chars"
	SettingHeadersMaxChars = "mail.headers.max_chars"
	// SettingInlinePartMaxBytes, SettingInlineTotalMaxBytes and
	// SettingInlineMaxParts cap the inline parts a message may carry.
	SettingInlinePartMaxBytes  = "mail.inline_assets.max_part_bytes"
	SettingInlineTotalMaxBytes = "mail.inline_assets.max_total_bytes"
	SettingInlineMaxParts      = "mail.inline_assets.max_parts"
)
