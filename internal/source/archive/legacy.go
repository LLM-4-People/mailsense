package archive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/logx"
	"github.com/LLM-4-People/mailsense/internal/store"
)

// LegacyAnswers configures ImportLegacyAnswers: the legacy client's
// answers (classifications.jsonl, plain or gzip) of the messages this
// archive source imported.
type LegacyAnswers struct {
	Path     string
	SourceID string // the archive source the messages came from
	// Fingerprint is the legacy question set the answers were made for:
	// a line of another fingerprint is never imported (counted).
	Fingerprint string
	// Decider is the key the answers are stored under: never a live
	// decider's (the answers were made from leaky legacy states and are
	// never mixed with live ones).
	Decider   string
	Questions []engine.Question
	// Tolerance bounds |Σ probabilities − 1| of a choice or score answer
	// (the provider's probability_tolerance).
	Tolerance float64
	// Skip names messages (by their source id) never imported.
	Skip    func(messageID string) bool
	MaxLine int
}

// LegacyReport counts what an import did (no message content).
type LegacyReport struct {
	Lines            int            `json:"lines"`
	Imported         int            `json:"imported"`
	BadLines         int            `json:"bad_lines"`
	OtherFingerprint int            `json:"other_fingerprint"`
	NoMessage        int            `json:"no_message"` // no message of the source with that id
	Skipped          int            `json:"skipped"`
	Dropped          map[string]int `json:"dropped"` // per question: invalid answers left out
	Missing          map[string]int `json:"missing"` // per question: no answer on the line
}

// legacyAnswer is one answer of the legacy client: model_value (a yes/no
// question's raw P(yes); a score's expected level; null for a choice)
// and the model's own probabilities (choice and score). Its value,
// prior and confidence are the legacy blend and are not read.
type legacyAnswer struct {
	ModelValue    *float64           `json:"model_value"`
	Probabilities map[string]float64 `json:"probabilities"`
}

// LegacyPrefix starts every legacy decider key: a live decider's key is
// "<provider>/<model>" and is never one.
const LegacyPrefix = "legacy:"

// IsLegacyKey reports whether a decider key names imported legacy
// answers (never a live decider's: no '/').
func IsLegacyKey(decider string) bool {
	return strings.HasPrefix(decider, LegacyPrefix) && len(decider) > len(LegacyPrefix) && !strings.Contains(decider, "/")
}

// ImportLegacyAnswers stores the legacy answers as triage decisions of
// o.Decider, replacing that key's decisions as a whole (one transaction:
// a failed import keeps the previous ones). Answers are the model's own
// (never the legacy blend), checked like a live answer: complete,
// finite probabilities summing to 1 within o.Tolerance (a choice is
// their argmax, a score its expected level); an invalid answer is left
// out and counted. These are local copies of answers already paid for:
// importing them never calls a provider.
func ImportLegacyAnswers(ctx context.Context, st *store.Store, o LegacyAnswers) (LegacyReport, error) {
	rep := LegacyReport{Dropped: map[string]int{}, Missing: map[string]int{}}
	if o.Decider == "" || o.Fingerprint == "" || len(o.Questions) == 0 {
		return rep, errors.New("archive: a legacy import needs a decider key, a fingerprint and the questions")
	}
	if !IsLegacyKey(o.Decider) {
		return rep, fmt.Errorf("archive: %q is not a legacy decider key (%q followed by a name, without '/'): a legacy import never writes a live decider's answers", o.Decider, LegacyPrefix)
	}
	type row struct{ id, revision int64 }
	rows := map[string]row{}
	err := st.EachMessage(ctx, store.MessageFilter{}, func(r *store.MessageRow) error {
		if r.Message.SourceID == o.SourceID {
			rows[r.Message.ID] = row{r.RowID, r.Revision}
		}
		return nil
	})
	if err != nil {
		return rep, err
	}
	var out []store.Decision
	err = EachLine(o.Path, o.MaxLine, func(_ int, line []byte) error {
		rep.Lines++
		var rec struct {
			Classification struct {
				MessageID   string                  `json:"message_id"`
				Fingerprint string                  `json:"fingerprint"`
				Answers     map[string]legacyAnswer `json:"answers"`
			} `json:"classification"`
		}
		if json.Unmarshal(line, &rec) != nil || rec.Classification.MessageID == "" {
			rep.BadLines++
			return nil
		}
		c := rec.Classification
		r, ok := rows[c.MessageID]
		switch {
		case c.Fingerprint != o.Fingerprint:
			rep.OtherFingerprint++
			return nil
		case !ok:
			rep.NoMessage++
			return nil
		case o.Skip != nil && o.Skip(c.MessageID):
			rep.Skipped++
			return nil
		}
		answers := map[string]engine.Answer{}
		for i := range o.Questions {
			q := &o.Questions[i]
			la, has := c.Answers[q.Name]
			if !has {
				rep.Missing[q.Name]++
				continue
			}
			a, err := la.answer(q, o.Tolerance)
			if err != nil {
				rep.Dropped[q.Name]++
				continue
			}
			answers[q.Name] = a
		}
		raw, err := json.Marshal(answers)
		if err != nil {
			return err
		}
		out = append(out, store.Decision{MessageID: r.id, Revision: r.revision, StateHash: "legacy:" + o.Fingerprint, Answers: string(raw)})
		return nil
	})
	if err != nil {
		return rep, err
	}
	if err := st.ReplaceDecisions(ctx, store.TriageKey{Fingerprint: o.Fingerprint, Decider: o.Decider}, out); err != nil {
		return rep, err
	}
	rep.Imported = len(out)
	logx.For("source.archive").Info("legacy answers imported", "decider", o.Decider, "fingerprint", o.Fingerprint, "imported", rep.Imported,
		"lines", rep.Lines, "bad_lines", rep.BadLines, "other_fingerprint", rep.OtherFingerprint, "no_message", rep.NoMessage, "skipped", rep.Skipped)
	return rep, nil
}

// answer is the legacy answer as the triage contract's, checked.
func (la legacyAnswer) answer(q *engine.Question, tolerance float64) (engine.Answer, error) {
	a := engine.Answer{Type: q.Type}
	switch q.Type {
	case engine.Noul:
		v := la.ModelValue
		if v == nil || math.IsNaN(*v) || *v < 0 || *v > 1 {
			return a, fmt.Errorf("archive: %s: no probability of yes", q.Name)
		}
		a.Value = *v
		return a, nil
	case engine.Choice:
		var keys []string
		for _, o := range q.Options {
			keys = append(keys, o.Name)
		}
		p, err := distribution(la.Probabilities, keys, tolerance)
		if err != nil {
			return a, fmt.Errorf("archive: %s: %w", q.Name, err)
		}
		best := 0
		for i := range keys {
			if p[keys[i]] > p[keys[best]] {
				best = i
			}
		}
		conf := p[keys[best]]
		a.Choice, a.Probabilities, a.Confidence = keys[best], p, &conf
		return a, nil
	}
	var keys []string
	for i := range q.Levels {
		keys = append(keys, engine.LevelKey(i))
	}
	p, err := distribution(la.Probabilities, keys, tolerance)
	if err != nil {
		return a, fmt.Errorf("archive: %s: %w", q.Name, err)
	}
	var conf float64
	for i, k := range keys {
		a.Value += float64(i) * p[k]
		conf = max(conf, p[k])
	}
	a.Probabilities, a.Confidence = p, &conf
	return a, nil
}

// distribution checks probabilities over exactly keys.
func distribution(p map[string]float64, keys []string, tolerance float64) (map[string]float64, error) {
	if len(p) != len(keys) {
		return nil, fmt.Errorf("%d probabilities for %d classes", len(p), len(keys))
	}
	var sum float64
	for k, v := range p {
		if !slices.Contains(keys, k) {
			return nil, fmt.Errorf("unknown class %q", k)
		}
		if math.IsNaN(v) || v < 0 || v > 1 {
			return nil, fmt.Errorf("probability %v", v)
		}
		sum += v
	}
	if math.Abs(sum-1) > tolerance {
		return nil, fmt.Errorf("probabilities sum to %v", sum)
	}
	return p, nil
}
