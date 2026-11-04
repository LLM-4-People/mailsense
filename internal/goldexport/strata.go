package goldexport

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Options are the export's parameters.
type Options struct {
	Size      int
	Seed      uint64
	RareFloor int    // a legacy category with fewer sampled messages is topped up to this (when it has them)
	Legacy    string // legacy classifications (JSONL, plain or gzip); "" = no category stratum
	Dir       string
}

// Check refuses options an export cannot run with.
func (o Options) Check() error {
	if o.Size < 1 || o.RareFloor < 0 || o.Dir == "" {
		return errors.New("goldexport: an export needs a size ≥ 1, a floor ≥ 0 and a directory")
	}
	return nil
}

// Key names a stratum.
type Key struct{ Script, Category, SenderKind, Year string }

func (k Key) String() string {
	return strings.Join([]string{k.Script, k.Category, k.SenderKind, k.Year}, "\x00")
}

// Stratum is one group of the eligible mail (aggregates only).
type Stratum struct {
	Script     string `json:"script"`
	Category   string `json:"category"`
	SenderKind string `json:"sender_kind"`
	Year       string `json:"year"`
	Population int    `json:"population"`
	Sampled    int    `json:"sampled"`
	rows       []int64
}

// Report is strata.json.
type Report struct {
	Eligible int       `json:"eligible"`
	Sampled  int       `json:"sampled"`
	Seed     uint64    `json:"seed"`
	Floor    int       `json:"rare_floor"`
	Strata   []Stratum `json:"strata"`
	// Categories is the population and sample per legacy category.
	Categories map[string][2]int `json:"categories,omitempty"`
	// LegacyBadLines counts the legacy classification lines that could
	// not be read.
	LegacyBadLines int `json:"legacy_bad_lines"`
}

// Strata groups the eligible rows.
type Strata struct{ by map[Key]*Stratum }

// NewStrata returns an empty grouping.
func NewStrata() *Strata { return &Strata{by: map[Key]*Stratum{}} }

// Add puts one eligible row into its stratum.
func (s *Strata) Add(k Key, row int64) {
	st := s.by[k]
	if st == nil {
		st = &Stratum{Script: k.Script, Category: k.Category, SenderKind: k.SenderKind, Year: k.Year}
		s.by[k] = st
	}
	st.rows = append(st.rows, row)
	st.Population++
}

// Report orders the strata (by key) into a report for the draw; it
// refuses a size larger than the eligible mail.
func (s *Strata) Report(o Options) (*Report, error) {
	rep := &Report{Seed: o.Seed, Floor: o.RareFloor}
	for _, k := range slices.SortedFunc(maps.Keys(s.by), func(a, b Key) int { return cmp.Compare(a.String(), b.String()) }) {
		rep.Strata = append(rep.Strata, *s.by[k])
		rep.Eligible += s.by[k].Population
	}
	if rep.Eligible < o.Size {
		return nil, fmt.Errorf("goldexport: %d eligible messages, the export needs %d", rep.Eligible, o.Size)
	}
	return rep, nil
}
