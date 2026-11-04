package config

import (
	"fmt"

	"github.com/LLM-4-People/mailsense/internal/fsx"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

// Overlay is shipped JSON data merged with the user's file named by a
// setting (a relative path is inside the data directory; a missing file
// means the shipped data, unchanged): the top-level members are replaced
// per key, the members of the objects under deep keys per name, and null
// removes one (jsonx.MergeObject). The one reader of the "shipped data +
// the user's override file" pattern; the caller decodes and validates.
func (c *Config) Overlay(setting string, shipped []byte, deep ...string) ([]byte, error) {
	data, _, err := c.OverlaySource(setting, shipped, deep...)
	return data, err
}

// OverlaySource is Overlay and the user's file it merged ("" when there
// is none: the shipped data as is).
func (c *Config) OverlaySource(setting string, shipped []byte, deep ...string) ([]byte, string, error) {
	path := c.Paths().Resolve(c.String(setting))
	over, ok, err := fsx.ReadOptional(path)
	if err != nil {
		return nil, "", fmt.Errorf("read %s (%s): %w", path, setting, err)
	}
	if !ok {
		return shipped, "", nil
	}
	data, err := jsonx.Overlay(shipped, over, deep...)
	if err != nil {
		return nil, "", fmt.Errorf("%s (%s): %w", path, setting, err)
	}
	return data, path, nil
}
