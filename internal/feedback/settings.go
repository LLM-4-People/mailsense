package feedback

import "github.com/LLM-4-People/mailsense/internal/config"

// Settings are every feedback.* setting, read from one snapshot.
type Settings struct {
	Correlations     CorrelationSettings
	Insights         InsightSettings
	CalibrationStep  float64
	CalibrationMin   int
	SuspiciousAddrIn int
	SuspiciousDomIn  int
	// EscalationAccuracy is the accuracy the answers kept (not escalated)
	// must reach for an escalation threshold to be suggested.
	EscalationAccuracy float64
}

// SettingsFrom reads the settings.
func SettingsFrom(cfg *config.Config) Settings {
	s := cfg.Snapshot()
	return Settings{
		Correlations: CorrelationSettings{MinSample: s.Int("feedback.min_sample"), MaxCells: s.Int("feedback.correlations.max_cells"),
			MeaningfulR: s.Float("feedback.correlations.meaningful_r")},
		Insights: InsightSettings{TopSenders: s.Int("feedback.insights.top_senders"), TopDomains: s.Int("feedback.insights.top_domains"),
			FasterFactor: s.Float("feedback.insights.faster_factor"), SlowerFactor: s.Float("feedback.insights.slower_factor"),
			KnownDomainMin: s.Int("feedback.insights.known_domain_min")},
		CalibrationStep: s.Float("feedback.calibration.step"), CalibrationMin: s.Int("feedback.calibration.min_labels"),
		SuspiciousAddrIn: s.Int("feedback.suspicious.address_min_labels"), SuspiciousDomIn: s.Int("feedback.suspicious.domain_min_labels"),
		EscalationAccuracy: s.Float("feedback.calibration.escalation_accuracy"),
	}
}
