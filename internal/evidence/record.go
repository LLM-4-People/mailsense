package evidence

import (
	"encoding/json"
	"fmt"

	"github.com/LLM-4-People/mailsense/internal/store"
)

// Record is one message's evidence: every signal of the current schema
// by name (a signal the stored row lacks is absent).
type Record map[string]Value

// encodeRecord is the stored form of a record under a schema.
func encodeRecord(s *Schema, r Record, reason func(string) (int64, error)) (string, error) {
	out := make([]json.RawMessage, len(s.Signals))
	for i, sig := range s.Signals {
		v, ok := r[sig.Name]
		if !ok {
			return "", fmt.Errorf("evidence: no value of %s", sig.Name)
		}
		raw, err := v.encode(sig.Type, reason)
		if err != nil {
			return "", fmt.Errorf("evidence: %s: %w", sig.Name, err)
		}
		out[i] = raw
	}
	b, err := json.Marshal(out)
	return string(b), err
}

// decodeRow reads a stored row: the values of the current schema's
// signals whose effective version is the stored one (fresh), the others
// left out.
//
// A row of another revision of the message than its current one is stale
// (the message changed since the last run): none of its values is
// current, so the record is empty until the next run.
func decodeRow(s *Schema, schemas map[int64]stored, row store.EvidenceRow, reason func(int64) (string, error)) (Record, error) {
	if row.Current != 0 && row.Current != row.Revision {
		return Record{}, nil
	}
	st, ok := schemas[row.SchemaID]
	if !ok {
		return nil, fmt.Errorf("evidence: message %d: unknown schema %d", row.MessageID, row.SchemaID)
	}
	var raw []json.RawMessage
	if err := json.Unmarshal([]byte(row.Values), &raw); err != nil {
		return nil, fmt.Errorf("evidence: message %d: %w", row.MessageID, err)
	}
	out := Record{}
	for i, sig := range s.Signals {
		at, ok := st[sig.Name]
		if !ok || at.key != s.Keys[i] || at.pos >= len(raw) {
			continue
		}
		v, err := decode(raw[at.pos], sig.Type, reason)
		if err != nil {
			return nil, fmt.Errorf("evidence: message %d, %s: %w", row.MessageID, sig.Name, err)
		}
		out[sig.Name] = v
	}
	return out, nil
}
