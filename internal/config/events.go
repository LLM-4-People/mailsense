package config

// SettingEventsBuffer is how many events each subscriber buffers before the
// new ones are dropped and counted. It is a transport fact, so consumers ask
// for the count rather than spelling the key (and its default) again.
const SettingEventsBuffer = "events.buffer_size"

// EventBuffer returns the subscriber buffer size of the current snapshot.
func (c *Config) EventBuffer() int { return c.Snapshot().EventBuffer() }

// EventBuffer returns the subscriber buffer size of this snapshot.
func (s *Snapshot) EventBuffer() int { return s.Int(SettingEventsBuffer) }
