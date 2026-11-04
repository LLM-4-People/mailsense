package evidence

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/digest"
)

// keyChars is the length of the settings digest in an effective version.
const keyChars = 8

// Schema is the ordered list of signals with their effective versions:
// "v<version>", plus a digest of the values of the settings and of the
// pinned data the signal reads. A stored value is reused only under the
// same effective version.
type Schema struct {
	Signals []Signal
	Keys    []string
	JSON    string // the stored form: [[name, key], ...]
}

// newSchema is the schema of the registry under a configuration.
func newSchema(signals []Signal, snap *config.Snapshot) (*Schema, error) {
	s := &Schema{Signals: signals}
	pairs := make([][2]string, len(signals))
	for i, sig := range signals {
		key := "v" + strconv.Itoa(sig.Version)
		if len(sig.Settings)+len(sig.Data) > 0 {
			vals := make([]any, 0, len(sig.Settings)+len(sig.Data))
			for _, k := range sig.Settings {
				v, ok := snap.Value(k)
				if !ok {
					return nil, fmt.Errorf("evidence: signal %s reads unknown setting %q", sig.Name, k)
				}
				vals = append(vals, v)
			}
			for _, d := range sig.Data {
				v, ok := dataDigest(d)
				if !ok {
					return nil, fmt.Errorf("evidence: signal %s reads unknown data %q", sig.Name, d)
				}
				vals = append(vals, d+"="+v)
			}
			raw, err := json.Marshal(vals)
			if err != nil {
				return nil, err
			}
			key += "+" + digest.Hex(raw, keyChars)
		}
		s.Keys = append(s.Keys, key)
		pairs[i] = [2]string{sig.Name, key}
	}
	raw, err := json.Marshal(pairs)
	s.JSON = string(raw)
	return s, err
}

// stored is a recorded schema read back: signal name → (position, key).
type stored map[string]struct {
	pos int
	key string
}

func parseStored(raw string) (stored, error) {
	var pairs [][2]string
	if err := json.Unmarshal([]byte(raw), &pairs); err != nil {
		return nil, fmt.Errorf("evidence: stored schema: %w", err)
	}
	out := stored{}
	for i, p := range pairs {
		out[p[0]] = struct {
			pos int
			key string
		}{i, p[1]}
	}
	return out, nil
}
