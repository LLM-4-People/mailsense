package evidence

import (
	"fmt"
	"runtime"
	"time"

	"github.com/LLM-4-People/mailsense/internal/clock"
	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/threads"
)

// Settings are the evidence.* settings (and the shared ones signals
// read), read once per run from one snapshot.
type Settings struct {
	Zone           *time.Location // time.zone: the owner's day that dates are resolved in
	MinLetters     int            // evidence.language.min_letters
	LangChars      int            // evidence.language.max_chars
	MaxEdits       int            // evidence.lookalike.max_edits
	MinNameChars   int            // evidence.lookalike.min_chars
	TemplateCosine float64        // evidence.template.min_cosine
	TemplateWindow int            // evidence.template.window
	MaxDates       int            // evidence.dates.max
	Batch          int            // evidence.batch_size
	Workers        int            // evidence.workers (0: every CPU)
	Domains        mail.Domains   // mail.domains.shared
	Threads        threads.Settings
}

// Setting keys signals declare (Signal.Settings).
const (
	settingMinLetters     = "evidence.language.min_letters"
	settingLangChars      = "evidence.language.max_chars"
	settingMaxEdits       = "evidence.lookalike.max_edits"
	settingMinNameChars   = "evidence.lookalike.min_chars"
	settingTemplateCosine = "evidence.template.min_cosine"
	settingTemplateWindow = "evidence.template.window"
	settingMaxDates       = "evidence.dates.max"
	settingBuckets        = threads.SettingBuckets
)

// contextSettings are the settings a thread or history signal reads:
// those the conversations and the history are built with
// (threads.GroupingSettings), plus its own.
func contextSettings(own ...string) []string {
	return append(threads.GroupingSettings(), own...)
}

// settingsFrom reads the settings; an unconfirmed time zone is an error
// (dates are resolved in the owner's day: never a guessed zone).
func settingsFrom(cfg *config.Config) (Settings, error) {
	th, err := threads.SettingsFrom(cfg)
	if err != nil {
		return Settings{}, fmt.Errorf("evidence: %w", err)
	}
	s := cfg.Snapshot()
	zone, err := clock.Zone(s.String(clock.SettingZone))
	if err != nil {
		return Settings{}, fmt.Errorf("evidence: %w", err)
	}
	workers := s.Int("evidence.workers")
	if workers == 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	return Settings{Zone: zone, MinLetters: s.Int(settingMinLetters), LangChars: s.Int(settingLangChars),
		MaxEdits: s.Int(settingMaxEdits), MinNameChars: s.Int(settingMinNameChars), TemplateCosine: s.Float(settingTemplateCosine),
		TemplateWindow: s.Int(settingTemplateWindow), MaxDates: s.Int(settingMaxDates), Batch: s.Int("evidence.batch_size"),
		Workers: workers, Domains: mail.NewDomains(s.Strings(mail.SettingSharedDomains)), Threads: th}, nil
}
