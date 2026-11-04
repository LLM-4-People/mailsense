package syncer

import "github.com/LLM-4-People/mailsense/internal/events"

// progress publishes p on the bus and hands it to the run's callback.
func (s *Syncer) progress(p Progress, report func(Progress)) {
	events.PublishProgress(s.bus, TopicProgress, p, report)
}
