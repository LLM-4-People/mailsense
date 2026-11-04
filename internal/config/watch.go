package config

import (
	"slices"
	"time"

	"github.com/LLM-4-People/mailsense/internal/events"
	"github.com/LLM-4-People/mailsense/internal/fsx"
)

// WatchDataFiles checks, every config.watch_interval, the content stamp
// (size and modification time) of every data file named by a DataFile
// setting, and publishes a TopicChanged naming the setting when it
// changed (rules review #3: an edited teachers file never started a run).
// The lowest choke point: every consumer that re-runs on a change of the
// setting re-runs on an edit of its file too. stop ends the check (0
// interval or no bus: nothing is checked).
func (c *Config) WatchDataFiles() (stop func()) {
	every := c.Snapshot().Duration("config.watch_interval")
	if every <= 0 || c.opts.Bus == nil {
		return func() {}
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	stamps := c.dataFileStamps()
	go func() {
		defer close(stopped)
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				now := c.dataFileStamps()
				var keys []string
				for k, s := range now {
					if stamps[k] != s {
						keys = append(keys, k)
					}
				}
				slices.Sort(keys)
				stamps = now
				if len(keys) > 0 {
					events.Publish(c.opts.Bus, TopicChanged, Change{Keys: keys})
				}
			}
		}
	}()
	return func() { close(done); <-stopped }
}

// dataFileStamps is every data file setting's current file stamp (the
// path is part of it: a setting pointed elsewhere counts too).
func (c *Config) dataFileStamps() map[string]string {
	s := c.Snapshot()
	out := map[string]string{}
	for _, st := range c.schema.Settings() {
		if st.DataFile {
			path := c.Paths().Resolve(s.String(st.Key))
			out[st.Key] = path + "|" + fsx.Stamp(path)
		}
	}
	return out
}
