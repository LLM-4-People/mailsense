package layer1

import (
	"math/rand/v2"
	"strconv"
	"testing"
	"time"

	"github.com/LLM-4-People/mailsense/internal/vector"
)

// benchDesign is a mailbox-sized synthetic design: n rows, a 768-d
// embedding and 584 sparse cells (≈ the fixture's layout).
func benchDesign(b testing.TB, n, classes int) (*Design, []Example) {
	r := rand.New(rand.NewPCG(11, 12))
	db := NewDesign(768, 584)
	var ex []Example
	for i := range n {
		c := r.IntN(classes)
		v := make([]float32, 768)
		for j := range v {
			v[j] = float32(r.NormFloat64())
		}
		v[c] += 6
		u, _ := vector.Truncate(v, 768)
		q := vector.Quantize(u)
		var cells []int32
		for k := 0; k < 584; k += 8 {
			cells = append(cells, int32(k+r.IntN(8)))
		}
		if err := db.Add(int64(i+1), &q, cells); err != nil {
			b.Fatal(err)
		}
		p := make([]float64, classes)
		for k := range p {
			p[k] = 0.02
		}
		p[c] = 1 - 0.02*float64(classes-1)
		t, _ := Soft(p, 1e-9)
		ex = append(ex, Example{Row: i, Target: t, Weight: 1})
	}
	d, _ := db.Build()
	return d, ex
}

// BenchmarkObjective is one loss + gradient evaluation of a 7-class
// head over 9,000 rows.
func BenchmarkObjective(b *testing.B) {
	d, ex := benchDesign(b, 9000, 7)
	h, _ := newHead(Multinomial, 7)
	ob := newObjective(d, h, ex, FitOptions{Lambda: 1e-3, Workers: 12, ShardRows: 512}, standardizerOf(d, ex))
	defer ob.Close()
	x := make([]float64, 7*d.Cols()+7)
	g := make([]float64, len(x))
	b.ResetTimer()
	for range b.N {
		ob.eval(x, g)
	}
}

// BenchmarkInfer is one message answered by 17 heads (the default
// profile's mix: 6 choice of 4–11 classes, 6 yes/no, 5 scores of 4–6
// levels) over a 768-d embedding and 584 cells: the per-message cost of
// layer 1's answers.
func BenchmarkInfer(b *testing.B) {
	d, ex := benchDesign(b, 64, 7)
	type spec struct {
		k       Kind
		classes int
	}
	var specs []spec
	for _, c := range []int{7, 11, 8, 8, 4, 4} {
		specs = append(specs, spec{Multinomial, c})
	}
	for range 6 {
		specs = append(specs, spec{Binary, 2})
	}
	for _, c := range []int{5, 6, 4, 5, 4} {
		specs = append(specs, spec{Ordinal, c})
	}
	tr := &Trained{Design: d, Models: map[string]*Model{}, Members: map[int64]Member{}, knn: map[string]*knnIndex{}}
	for i, s := range specs {
		h, _ := newHead(s.k, s.classes)
		m := &Model{Question: strconv.Itoa(i), Kind: s.k, Classes: s.classes, Combine: Identity,
			Params: Params{W: make([]float64, h.outputs()*d.Cols()), B: make([]float64, h.outputs()), Extra: make([]float64, h.extra())}}
		h.init(Prior([]Example{{Target: OneHot(0), Weight: 1}, {Target: OneHot(1), Weight: 1}}, s.classes), m.Params.B, m.Params.Extra)
		m.fold(standardizerOf(d, ex))
		tr.Models[m.Question] = m
	}
	b.ResetTimer()
	for i := range b.N {
		id := d.IDs[i%len(d.IDs)]
		for q := range tr.Models {
			if _, ok := tr.Predict(q, id); !ok {
				b.Fatal("no prediction")
			}
		}
	}
}

// BenchmarkKNN is one message's causal kNN distribution over 34,000
// labelled 768-d messages (the fixture's size).
func BenchmarkKNN(b *testing.B) {
	r := rand.New(rand.NewPCG(5, 5))
	set := &vector.Set{}
	x := newKNN(set, KNNConfig{K: 20, Temperature: 0.05, Prior: 1}, []float64{0.5, 0.5})
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range 34000 {
		v := make([]float32, 768)
		for j := range v {
			v[j] = float32(r.NormFloat64())
		}
		u, _ := vector.Truncate(v, 768)
		q := vector.Quantize(u)
		set.Put(int64(i), q.Values, q.Scale)
		x.add(int64(i), OneHot(i%2), 1, t0.Add(time.Duration(i)*time.Minute), strconv.Itoa(i/3))
	}
	out := make([]float64, 2)
	b.ResetTimer()
	for i := range b.N {
		id := int64(20000 + i%10000)
		x.logProbs(id, t0.Add(time.Duration(id)*time.Minute), strconv.Itoa(int(id)/3), nil, out)
	}
}
