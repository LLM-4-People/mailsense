package evidence

import (
	"context"
	"strconv"
	"strings"
)

// FactsText renders a record as observed facts for a triage state (role
// b): one "name: value" line per causal signal with a value, in name
// order — numbers as their buckets (features.json), so the text is
// stable; a value read on a cut input says so; opaque values (hashes),
// not-applicable and unavailable values are left out. Nothing here is
// ever an intent or a word of the message.
func (e *Engine) FactsText(r Record) string {
	var b strings.Builder
	for _, s := range e.signals {
		v, ok := r[s.Name]
		if !s.Causal || s.Opaque || !ok || v.State != OK {
			continue
		}
		b.WriteString(s.Name)
		b.WriteString(": ")
		b.WriteString(e.factValue(s, v))
		if v.Partial {
			b.WriteString(" (input cut: at least this, or not seen in the part read)")
		}
		b.WriteByte('\n')
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func (e *Engine) factValue(s Signal, v Value) string {
	switch s.Type {
	case TypeBool:
		if v.Bool {
			return "yes"
		}
		return "no"
	case TypeEnum, TypeCategory:
		if s.Shown != nil {
			return s.Shown(v.Str)
		}
		return v.Str
	case TypeSet:
		if len(v.List) == 0 {
			return "none"
		}
		return strings.Join(v.List, ", ")
	}
	return e.bins.label(s, e.bins.bucket(s, v))
}

// Facts renders the facts of every visible message (triage reads them
// when triage.facts.evidence is on).
func (e *Engine) Facts(ctx context.Context) (map[int64]string, error) {
	out := map[int64]string{}
	err := e.EachRecord(ctx, func(id int64, r Record) error {
		out[id] = e.FactsText(r)
		return nil
	})
	return out, err
}

// Version changes whenever a run wrote evidence in this process (views
// built from the evidence, such as triage states with facts, are rebuilt).
func (e *Engine) Version() string { return strconv.FormatInt(e.writes.Load(), 10) }
