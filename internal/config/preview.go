package config

// SettingPreview unlocks the explicit test preview: automatic answers stay
// uncertified and every answer screen says so. It is a learning fact, so
// services ask for it instead of each reading the key (and deciding on their
// own whether a nil config means off).
const SettingPreview = "learning.test_mode"

// Preview reports the test-preview switch; a nil config is off.
func (c *Config) Preview() bool { return c != nil && c.Bool(SettingPreview) }
