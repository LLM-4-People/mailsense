package mail

import "github.com/LLM-4-People/mailsense/internal/config"

// Limits are the per-message size caps every connector applies while reading
// a message: the one owner of the six mail.* connector settings. Outlook and
// the RFC 5322 reader both consume them, so a cap cannot be typed as a second
// literal in one connector and drift from the other.
type Limits struct {
	BodyChars        int // mail.body.max_chars
	HTMLChars        int // mail.body.html_max_chars
	HeaderChars      int // mail.headers.max_chars
	InlinePartBytes  int // mail.inline_assets.max_part_bytes
	InlineTotalBytes int // mail.inline_assets.max_total_bytes
	InlineParts      int // mail.inline_assets.max_parts
}

// LimitsFromConfig reads the six mail.* connector caps.
func LimitsFromConfig(s *config.Snapshot) Limits {
	return Limits{
		BodyChars:        s.Int(SettingBodyMaxChars),
		HTMLChars:        s.Int(SettingHTMLMaxChars),
		HeaderChars:      s.Int(SettingHeadersMaxChars),
		InlinePartBytes:  s.Int(SettingInlinePartMaxBytes),
		InlineTotalBytes: s.Int(SettingInlineTotalMaxBytes),
		InlineParts:      s.Int(SettingInlineMaxParts),
	}
}
