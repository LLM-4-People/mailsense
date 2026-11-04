package index

import (
	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/syncer"
)

// AfterSync starts an index run (a job) after every sync run that advanced
// the stored mail (syncer.Report.Advanced) while index.after_sync is on: new
// and changed mail is indexed without anyone asking. A sync that finishes
// during a run is not lost: one more run follows the current one
// (jobs.Single). stop ends the subscription; phase 4's UI shows the runs
// through jobs.update and index.finished.
func (ix *Indexer) AfterSync(bus *events.Bus, m *jobs.Manager) (stop func(), err error) {
	return events.On(bus, syncer.TopicFinished, ix.cfg.EventBuffer(), func(rep syncer.Report) {
		if !rep.Advanced() || !ix.cfg.Bool(SettingAfterSync) {
			return
		}
		ix.single.Want(func() {
			if _, err := ix.startClaimed(m, Options{}); err != nil {
				ix.log.Warn("index run after sync not started", "source", rep.SourceID, "err", logx.RedactedError(err))
			}
		})
	})
}
