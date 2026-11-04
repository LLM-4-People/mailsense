package evidence

import (
	"context"
	"strconv"
)

// SignalCoverage is one signal's coverage and distribution over the
// visible mail: how many messages have a value (complete or read on a cut
// input), how many none and why, and the values — counts per value of an
// enum, a truth value or a readable category or set; per bucket of a
// number (features.json) with its mean and maximum; only the number of
// distinct values for an opaque one (never a hash list).
type SignalCoverage struct {
	Name        string         `json:"name"`
	Group       string         `json:"group"`
	Causal      bool           `json:"causal"`
	OK          int            `json:"ok"`
	Partial     int            `json:"partial"` // of OK: read on a cut input
	NA          int            `json:"not_applicable"`
	Unavailable int            `json:"unavailable"`
	NotComputed int            `json:"not_computed"` // stored under another effective version
	Reasons     map[string]int `json:"reasons,omitempty"`
	Values      map[string]int `json:"values,omitempty"`
	Distinct    int            `json:"distinct,omitempty"`
	Mean        float64        `json:"mean,omitempty"`
	Max         float64        `json:"max,omitempty"`
	distinct    map[string]bool
	sum         float64
}

// Coverage is the coverage of every signal (the UI's evidence view and the
// fixture report).
type Coverage struct {
	Messages int              `json:"messages"`
	Signals  []SignalCoverage `json:"signals"`
}

// Coverage aggregates the stored evidence of every visible message.
func (e *Engine) Coverage(ctx context.Context) (Coverage, error) {
	out := Coverage{Signals: make([]SignalCoverage, len(e.signals))}
	for i, s := range e.signals {
		out.Signals[i] = SignalCoverage{Name: s.Name, Group: s.Group, Causal: s.Causal, Reasons: map[string]int{}, Values: map[string]int{},
			distinct: map[string]bool{}}
	}
	err := e.EachRecord(ctx, func(_ int64, r Record) error {
		out.Messages++
		for i, s := range e.signals {
			e.count(&out.Signals[i], s, r)
		}
		return nil
	})
	for i, s := range e.signals {
		c := &out.Signals[i]
		if s.Opaque {
			c.Distinct, c.Values = len(c.distinct), nil
		}
		if numeric(s.Type) && c.OK > 0 {
			c.Mean = c.sum / float64(c.OK)
		}
	}
	return out, err
}

func (e *Engine) count(c *SignalCoverage, s Signal, r Record) {
	v, ok := r[s.Name]
	switch {
	case !ok:
		c.NotComputed++
		return
	case v.State == NA:
		c.NA++
		c.Reasons["na:"+v.Reason]++
		return
	case v.State == Unavailable:
		c.Unavailable++
		c.Reasons["unavailable:"+v.Reason]++
		return
	}
	c.OK++
	if v.Partial {
		c.Partial++
	}
	switch s.Type {
	case TypeBool:
		c.Values[strconv.FormatBool(v.Bool)]++
	case TypeEnum, TypeCategory:
		shown := v.Str
		if s.Shown != nil {
			shown = s.Shown(v.Str)
		}
		c.Values[shown]++
		c.distinct[v.Str] = true
	case TypeSet:
		for _, x := range v.List {
			c.Values[x]++
			c.distinct[x] = true
		}
		if len(v.List) == 0 {
			c.Values["(empty)"]++
		}
	default:
		x := number(s, v)
		if c.sum += x; c.OK == 1 || x > c.Max {
			c.Max = x
		}
		c.Values[e.bins.label(s, e.bins.bucket(s, v))]++
	}
}
