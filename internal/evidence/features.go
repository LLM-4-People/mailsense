package evidence

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"slices"
	"strconv"

	"github.com/LLM-4-People/mailsense/internal/config"
	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/jsonx"
)

//go:embed features.json
var featuresJSON []byte

// The feature-layout settings layer 1's route key reads. The file name alone
// is not enough: its merged content is the input, and FeaturesDigest covers
// both.
const (
	SettingFeaturesFile        = "evidence.features.file"
	SettingFeaturesHashBuckets = "evidence.features.hash_buckets"
)

// Bins are the bucket bounds of the numeric signals (features.json). Digest
// is the identity of the merged document, part of the route key.
type Bins struct {
	Digest string
	bounds map[string][]float64
}

// LoadBins decodes features.json merged with evidence.features.file (per
// signal; null removes) and checks it against the registry: every numeric
// signal has ascending bounds, no bounds name an unknown signal.
func LoadBins(cfg *config.Config, signals []Signal) (*Bins, error) {
	data, err := withOverrides(cfg, SettingFeaturesFile, featuresJSON, "bins")
	if err != nil {
		return nil, err
	}
	var doc struct {
		Doc  string               `json:"_doc"`
		Bins map[string][]float64 `json:"bins"`
	}
	if err := jsonx.DecodeStrict(data, &doc); err != nil {
		return nil, fmt.Errorf("evidence: features: %w", err)
	}
	var errs []error
	known := map[string]bool{}
	for _, s := range signals {
		known[s.Name] = true
		b, ok := doc.Bins[s.Name]
		if numeric(s.Type) && (!ok || len(b) == 0 || !slices.IsSorted(b)) {
			errs = append(errs, fmt.Errorf("evidence: features.json: %s needs ascending bounds", s.Name))
		}
	}
	for name := range doc.Bins {
		if !known[name] {
			errs = append(errs, fmt.Errorf("evidence: features.json: unknown signal %q", name))
		}
	}
	return &Bins{Digest: digest.Hex(data, 32), bounds: doc.Bins}, errors.Join(errs...)
}

func numeric(t Type) bool { return t == TypeInt || t == TypeFloat || t == TypeInts }

// FeaturesDigest is the identity of every input of the feature layout the
// models read: the merged bins document, the signal schema (its versions,
// settings and pinned data) and the hash-bucket count. A change to any of
// them changes what a trained model means, so the route key must move and
// the next pass must retrain.
func FeaturesDigest(cfg *config.Config) (string, error) {
	bins, err := LoadBins(cfg, Signals())
	if err != nil {
		return "", err
	}
	schema, err := newSchema(Signals(), cfg.Snapshot())
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(struct {
		Bins        string `json:"bins"`
		Schema      string `json:"schema"`
		HashBuckets int    `json:"hash_buckets"`
	}{Bins: bins.Digest, Schema: schema.JSON, HashBuckets: cfg.Snapshot().Int(SettingFeaturesHashBuckets)})
	if err != nil {
		return "", err
	}
	return digest.Hex(raw, 32), nil
}

// bucket is the bucket of a numeric value (a list: its length).
func (b *Bins) bucket(s Signal, v Value) int {
	x := number(s, v)
	k := 0
	for _, bound := range b.bounds[s.Name] {
		if x >= bound {
			k++
		}
	}
	return k
}

// label names a numeric bucket by its bounds ("<1", "1-2", "≥25").
func (b *Bins) label(s Signal, k int) string {
	bs := b.bounds[s.Name]
	f := func(x float64) string { return strconv.FormatFloat(x, 'f', -1, 64) }
	switch {
	case k == 0:
		return "<" + f(bs[0])
	case k >= len(bs):
		return "≥" + f(bs[len(bs)-1])
	}
	return f(bs[k-1]) + "-" + f(bs[k])
}

// FeatureLayout is one fixed layout of the feature view (layer 1's
// input): per causal signal, in name order, its one-hot cells (bool: the
// value; numeric: one per bucket; enum: one per value; category and set:
// evidence.features.hash_buckets hashed cells, read once for the layout),
// then "~partial" and "?missing". ID changes with every cell name (a
// vocabulary from data, such as the languages, versions it too).
type FeatureLayout struct {
	Names       []string
	ID          string
	e           *Engine
	hashBuckets int
}

// FeatureLayout is the current layout.
func (e *Engine) FeatureLayout() *FeatureLayout {
	l := &FeatureLayout{e: e, hashBuckets: e.cfg.Int(SettingFeaturesHashBuckets)}
	for _, s := range e.signals {
		if !s.Causal {
			continue
		}
		switch s.Type {
		case TypeBool:
			l.Names = append(l.Names, s.Name)
		case TypeEnum:
			for _, v := range s.Values {
				l.Names = append(l.Names, s.Name+"="+v)
			}
		case TypeCategory, TypeSet:
			for k := range l.hashBuckets {
				l.Names = append(l.Names, s.Name+"#"+strconv.Itoa(k))
			}
		default:
			for k := range len(e.bins.bounds[s.Name]) + 1 {
				l.Names = append(l.Names, s.Name+":"+e.bins.label(s, k))
			}
		}
		l.Names = append(l.Names, s.Name+"~partial", s.Name+"?missing")
	}
	raw, _ := json.Marshal(l.Names)
	l.ID = digest.Hex(raw, keyChars)
	return l
}

// Vector is the feature vector of a record in the layout: a signal
// without a usable value (not applicable, unavailable, not computed, an
// enum value outside its vocabulary) sets only its "?missing" cell.
func (l *FeatureLayout) Vector(r Record) []float32 {
	out := make([]float32, len(l.Names))
	for _, i := range l.Indices(r) {
		out[i] = 1
	}
	return out
}

// Indices are the cells Vector sets, ascending (the sparse form layer 1
// reads: every cell is 0 or 1).
func (l *FeatureLayout) Indices(r Record) []int32 {
	var out []int32
	base := 0
	for _, s := range l.e.signals {
		if !s.Causal {
			continue
		}
		cells := l.e.cells(s, l.hashBuckets)
		row := make([]float32, cells+2)
		if v, ok := r[s.Name]; !ok || v.State != OK || !l.e.oneHot(s, v, row, l.hashBuckets) {
			row = make([]float32, cells+2)
			row[cells+1] = 1
		} else if v.Partial {
			row[cells] = 1
		}
		for i, x := range row {
			if x != 0 {
				out = append(out, int32(base+i))
			}
		}
		base += cells + 2
	}
	return out
}

// oneHot sets a value's cells; false when the value has none.
func (e *Engine) oneHot(s Signal, v Value, row []float32, hashBuckets int) bool {
	switch s.Type {
	case TypeBool:
		if v.Bool {
			row[0] = 1
		}
	case TypeEnum:
		i := slices.Index(s.Values, v.Str)
		if i < 0 {
			return false
		}
		row[i] = 1
	case TypeCategory:
		row[hashCell(v.Str, hashBuckets)] = 1
	case TypeSet:
		for _, x := range v.List {
			row[hashCell(x, hashBuckets)] = 1
		}
	default:
		row[e.bins.bucket(s, v)] = 1
	}
	return true
}

func (e *Engine) cells(s Signal, hashBuckets int) int {
	switch s.Type {
	case TypeBool:
		return 1
	case TypeEnum:
		return len(s.Values)
	case TypeCategory, TypeSet:
		return hashBuckets
	}
	return len(e.bins.bounds[s.Name]) + 1
}

// hashCell is the hashed cell of a categorical value (FNV-1a: stable).
func hashCell(v string, n int) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(v))
	return int(h.Sum32() % uint32(max(n, 1)))
}
