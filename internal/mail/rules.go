package mail

import "github.com/LLM-4-People/mailsense/internal/config"

// TimesFromConfig reads the mail.no_date.* settings.
func TimesFromConfig(s *config.Snapshot) Times {
	return Times{NoDateYearAtMost: s.Int("mail.no_date.year_at_most"), NoDateYearAtLeast: s.Int("mail.no_date.year_at_least")}
}
