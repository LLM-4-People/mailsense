package index

import (
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/jobs"
	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/runstate"
	"github.com/LLM-4-People/mailsense/internal/store"
	"github.com/LLM-4-People/mailsense/internal/syncer"
)

// The index trigger follows the mail that changed: a partial sync (some
// folders failed) still indexes what arrived, while an output failure and a
// no-op run do not.
func TestIndexFollowsAdvancedSyncs(t *testing.T) {
	cases := []struct {
		name string
		rep  syncer.Report
		want bool
	}{
		{"partial with a write", syncer.Report{Status: runstate.Failed, Partial: true, Totals: store.RunTotals{Written: 1}}, true},
		{"output failure with a write", syncer.Report{Status: runstate.Failed, Totals: store.RunTotals{Written: 1}}, false},
		{"successful no-op", syncer.Report{Status: runstate.Succeeded}, false},
		{"successful write", syncer.Report{Status: runstate.Succeeded, Totals: store.RunTotals{Written: 1}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t, nil)
			w.mail("a", mail.RoleInbox, 1, "alpha")
			m := jobs.NewManager(w.cfg, w.bus, nil)
			t.Cleanup(func() { _ = m.Shutdown(t.Context()) })
			fin, err := events.Subscribe(w.bus, TopicFinished, 4)
			if err != nil {
				t.Fatal(err)
			}
			stop, err := w.ix.AfterSync(w.bus, m)
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			events.Publish(w.bus, syncer.TopicFinished, c.rep)
			if c.want {
				select {
				case rep := <-fin.C:
					if rep.Embedded != 1 {
						t.Fatalf("the sync's mail is indexed: %+v", rep)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("no index run after a sync that advanced the mail")
				}
				if got := m.List(); len(got) != 1 {
					t.Fatalf("exactly one index job must follow: %+v", got)
				}
				return
			}
			stop() // every handler drained: the event was handled
			if got := m.List(); len(got) != 0 {
				t.Fatalf("a sync that did not advance started %d index job(s): %+v", len(got), got)
			}
			if len(fin.C) != 0 {
				t.Fatal("a sync that did not advance produced an index run")
			}
		})
	}
}
