package evidence

import (
	"fmt"

	"github.com/LLM-4-People/mailsense/internal/config"
)

// withOverrides is shipped data merged with the user's file named by a
// setting (a relative path is inside the data directory; a missing file
// means the shipped data): the members of the object under key are
// replaced per name, null removes one.
func withOverrides(cfg *config.Config, setting string, shipped []byte, key string) ([]byte, error) {
	out, err := cfg.Overlay(setting, shipped, key)
	if err != nil {
		return nil, fmt.Errorf("evidence: %w", err)
	}
	return out, nil
}
