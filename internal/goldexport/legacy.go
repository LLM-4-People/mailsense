package goldexport

import (
	"encoding/json"

	"github.com/LLM-4-People/mailsense/internal/source/archive"
)

// Legacy are the legacy classifications' categories (message id → its
// category answer) and the lines that could not be read (counted and
// reported in strata.json, never silently skipped).
type Legacy struct {
	Categories map[string]string
	BadLines   int
}

// LegacyCategories reads the legacy classifications (JSONL, plain or
// gzip); "" = none.
func LegacyCategories(path string, maxLine int) (Legacy, error) {
	out := Legacy{Categories: map[string]string{}}
	if path == "" {
		return out, nil
	}
	err := archive.EachLine(path, maxLine, func(_ int, line []byte) error {
		var rec struct {
			Classification struct {
				MessageID string `json:"message_id"`
				Answers   map[string]struct {
					Value json.RawMessage `json:"value"`
				} `json:"answers"`
			} `json:"classification"`
		}
		if json.Unmarshal(line, &rec) != nil || rec.Classification.MessageID == "" {
			out.BadLines++
			return nil
		}
		var v string
		if json.Unmarshal(rec.Classification.Answers["category"].Value, &v) == nil && v != "" {
			out.Categories[rec.Classification.MessageID] = v
		}
		return nil
	})
	return out, err
}
