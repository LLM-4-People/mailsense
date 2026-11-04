package layer1

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"

	"github.com/LLM-4-People/mailsense/internal/optim"
)

// Example is one training item: a design row, its target and weight.
type Example struct {
	Row    int
	Target Target
	Weight float64
}

// FitOptions tune one fit.
type FitOptions struct {
	Lambda    float64 // L2 penalty on the standardised weights (biases and cut points are free)
	Optim     optim.Options
	Workers   int // goroutines (≥ 1)
	ShardRows int // rows per gradient shard: the reduction order, so the result never depends on Workers
}

// standardizer is one fit's column mean and inverse σ, computed from that
// fit's own examples. It is deliberately not a Design property: a design-wide
// statistic would carry the certification rows into the weights that predict
// them, so C would no longer be out of sample.
type standardizer struct {
	mean []float64
	inv  []float64
}

// standardizerOf computes the per-column mean and inverse σ of ex. Embedding
// values are read at the row's own scale; sparse cells are 0/1.
func standardizerOf(d *Design, ex []Example) standardizer {
	cols := d.Cols()
	sum, sq := make([]float64, cols), make([]float64, cols)
	n := float64(len(ex))
	for _, e := range ex {
		emb, scale := d.emb(e.Row)
		for j, x := range emb {
			v := float64(x) * scale
			sum[j] += v
			sq[j] += v * v
		}
		for _, c := range d.cellsOf(e.Row) {
			sum[d.Dims+int(c)]++
			sq[d.Dims+int(c)]++
		}
	}
	out := standardizer{mean: make([]float64, cols), inv: make([]float64, cols)}
	for j := range cols {
		m := sum[j] / n
		v := sq[j]/n - m*m
		out.mean[j] = m
		if v > 1e-12*math.Max(1, m*m) {
			out.inv[j] = 1 / math.Sqrt(v)
		}
	}
	return out
}

// ErrSingleClass: the targets put (almost) every weight on one class:
// nothing to learn.
var ErrSingleClass = errors.New("layer1: the targets hold a single class")

// Params are a head's parameters in the standardised space: W (cols ×
// outputs, column-major: W[j·outputs + k]), the biases and the extra
// parameters.
type Params struct {
	W, B, Extra []float64
}

func (p Params) flat() []float64 {
	return append(append(append([]float64(nil), p.W...), p.B...), p.Extra...)
}

func (p *Params) unflat(x []float64, outputs, cols int) {
	n := outputs * cols
	p.W, p.B, p.Extra = append(p.W[:0], x[:n]...), append(p.B[:0], x[n:n+outputs]...), append(p.Extra[:0], x[n+outputs:]...)
}

// Prior is the weighted class distribution of the examples.
func Prior(ex []Example, classes int) []float64 {
	p := make([]float64, classes)
	var sum float64
	for _, e := range ex {
		for c, m := range e.Target.Mass(classes) {
			p[c] += e.Weight * m
			sum += e.Weight * m
		}
	}
	for c := range p {
		p[c] /= math.Max(sum, 1e-300)
	}
	return p
}

// singleClass reports whether every class but one holds less than one
// example's worth of weight (0.999 of the largest example weight: a
// rounding slack of the float sums, an algorithm constant).
func singleClass(ex []Example, classes int) bool {
	mass := make([]float64, classes)
	maxW := 0.0
	for _, e := range ex {
		for c, m := range e.Target.Mass(classes) {
			mass[c] += e.Weight * m
		}
		maxW = math.Max(maxW, e.Weight)
	}
	present := 0
	for _, m := range mass {
		if m >= maxW*0.999 && m > 0 {
			present++
		}
	}
	return present < 2
}

// Fit minimises the weighted soft cross-entropy plus the L2 penalty from
// start (nil: zero weights, biases and cut points at the prior).
func Fit(ctx context.Context, d *Design, h head, ex []Example, start *Params, o FitOptions) (Params, optim.Result, error) {
	if len(ex) == 0 {
		return Params{}, optim.Result{}, errors.New("layer1: no examples")
	}
	for _, e := range ex {
		if err := e.Target.Check(h.classes()); err != nil {
			return Params{}, optim.Result{}, err
		}
		if e.Row < 0 || e.Row >= d.Len() || !(e.Weight > 0) {
			return Params{}, optim.Result{}, fmt.Errorf("layer1: example row %d weight %v", e.Row, e.Weight)
		}
	}
	if singleClass(ex, h.classes()) {
		return Params{}, optim.Result{}, ErrSingleClass
	}
	cols, outs := d.Cols(), h.outputs()
	p := Params{W: make([]float64, outs*cols), B: make([]float64, outs), Extra: make([]float64, h.extra())}
	if start != nil && len(start.W) == len(p.W) && len(start.B) == len(p.B) && len(start.Extra) == len(p.Extra) {
		copy(p.W, start.W)
		copy(p.B, start.B)
		copy(p.Extra, start.Extra)
	} else {
		h.init(Prior(ex, h.classes()), p.B, p.Extra)
	}
	obj := newObjective(d, h, ex, o, standardizerOf(d, ex))
	defer obj.Close()
	res, err := optim.Minimize(ctx, obj.eval, p.flat(), o.Optim)
	if res.X != nil {
		p.unflat(res.X, outs, cols)
	}
	return p, res, err
}

// objective is the sharded loss and gradient of one fit.
type objective struct {
	d      *Design
	h      head
	std    standardizer
	ex     []Example
	o      FitOptions
	total  float64 // Σ weights
	shards [][2]int
	bufs   [][]float64 // per shard: gradient (flat layout) then the loss

	// eff and scratch are reused across evaluations: the optimizer calls
	// eval serially, so one folded weight matrix and one score buffer per
	// shard are enough. Reusing them removes the per-evaluation
	// allocation that dominated the GC profile.
	eff     folded
	extra   []float64
	scratch []shardScratch
	// The worker pool is created once per fit. Spawning goroutines and a
	// channel per evaluation cost more than the small shards they
	// dispatched; a long-lived pool keeps the arithmetic busy.
	jobs chan shardJob
	wg   sync.WaitGroup
}

// shardScratch is one shard's reusable working set.
type shardScratch struct {
	z  []float64
	dz []float64
	r  []float64
}

// shardJob is one unit of work for the pool. The WaitGroup makes the pool
// reusable without a channel round-trip per evaluation.
type shardJob struct {
	s  int
	wg *sync.WaitGroup
}

func newObjective(d *Design, h head, ex []Example, o FitOptions, std standardizer) *objective {
	ob := &objective{d: d, h: h, std: std, ex: ex, o: o}
	for _, e := range ex {
		ob.total += e.Weight
	}
	size := max(o.ShardRows, 1)
	for lo := 0; lo < len(ex); lo += size {
		ob.shards = append(ob.shards, [2]int{lo, min(lo+size, len(ex))})
	}
	outs := h.outputs()
	n := outs*d.Cols() + outs + h.extra()
	ob.bufs = make([][]float64, len(ob.shards))
	ob.scratch = make([]shardScratch, len(ob.shards))
	for i := range ob.bufs {
		ob.bufs[i] = make([]float64, n+1)
		sc := &ob.scratch[i]
		sc.z = make([]float64, outs)
		sc.dz = make([]float64, outs)
		sc.r = make([]float64, outs)
	}
	ob.jobs = make(chan shardJob)
	ob.wg.Add(max(o.Workers, 1))
	for range max(o.Workers, 1) {
		go func() {
			defer ob.wg.Done()
			for job := range ob.jobs {
				ob.shard(job.s)
				job.wg.Done()
			}
		}()
	}
	return ob
}

// Close stops the worker pool. Every Fit owns one objective and must call
// it once; the optimizer calls eval serially, so no work is in flight.
func (ob *objective) Close() {
	close(ob.jobs)
	ob.wg.Wait()
}

// eval is the objective at x; grad receives its gradient. Shards are
// computed in parallel and summed in shard order (deterministic).
func (ob *objective) eval(x, grad []float64) float64 {
	cols, outs := ob.d.Cols(), ob.h.outputs()
	nw := outs * cols
	ob.effective(x[:nw], x[nw:nw+outs])
	ob.extra = x[nw+outs:]
	var wg sync.WaitGroup
	wg.Add(len(ob.shards))
	for s := range ob.shards {
		ob.jobs <- shardJob{s: s, wg: &wg}
	}
	wg.Wait()
	for i := range grad {
		grad[i] = 0
	}
	var loss float64
	for _, b := range ob.bufs {
		for i := range grad {
			grad[i] += b[i]
		}
		loss += b[len(b)-1]
	}
	// back from the folded (raw) space to the standardised weights:
	// ∂/∂w_kj = inv_j·(Σ r x_j − μ_j Σ r); the bias gradient is Σ r.
	for k := range outs {
		rk := grad[nw+k]
		for j := range cols {
			grad[j*outs+k] = ob.std.inv[j] * (grad[j*outs+k] - ob.std.mean[j]*rk)
		}
	}
	scale := 1 / ob.total
	for i := range grad {
		grad[i] *= scale
	}
	loss *= scale
	for i := range nw {
		loss += 0.5 * ob.o.Lambda * x[i] * x[i]
		grad[i] += ob.o.Lambda * x[i]
	}
	return loss
}

// folded is the head's weights in the raw space: u = w·inv per column
// and the bias b − Σ u·μ (standardisation folded in).
type folded struct {
	u    []float64
	bias []float64
}

// effective fills ob.eff in place, resizing only when the layout grows.
func (ob *objective) effective(w, b []float64) {
	outs, cols := ob.h.outputs(), ob.d.Cols()
	n := outs * cols
	if cap(ob.eff.u) < n {
		ob.eff.u = make([]float64, n)
	}
	ob.eff.u = ob.eff.u[:n]
	if cap(ob.eff.bias) < outs {
		ob.eff.bias = make([]float64, outs)
	}
	ob.eff.bias = ob.eff.bias[:outs]
	copy(ob.eff.bias, b)
	for k := range outs {
		for j := range cols {
			u := w[j*outs+k] * ob.std.inv[j]
			ob.eff.u[j*outs+k] = u
			ob.eff.bias[k] -= u * ob.std.mean[j]
		}
	}
}

func fold(std standardizer, outs int, w, b []float64) folded {
	cols := len(std.mean)
	f := folded{u: make([]float64, outs*cols), bias: append([]float64(nil), b...)}
	for k := range outs {
		for j := range cols {
			u := w[j*outs+k] * std.inv[j]
			f.u[j*outs+k] = u
			f.bias[k] -= u * std.mean[j]
		}
	}
	return f
}

// scores writes row i's linear outputs under folded weights (column-
// major weights: one pass over the row for every output).
func scores(d *Design, f folded, outs, i int, z []float64) {
	e, s := d.emb(i)
	for k := range outs {
		z[k] = 0
	}
	for j, v := range e {
		x := float64(v)
		u := f.u[j*outs : (j+1)*outs]
		for k := range u {
			z[k] += x * u[k]
		}
	}
	for k := range outs {
		z[k] *= s
	}
	for _, c := range d.cellsOf(i) {
		u := f.u[(d.Dims+int(c))*outs : (d.Dims+int(c)+1)*outs]
		for k := range u {
			z[k] += u[k]
		}
	}
	for k := range outs {
		z[k] += f.bias[k]
	}
}

// shard accumulates one shard's loss and raw-space gradient sums.
func (ob *objective) shard(s int) {
	buf := ob.bufs[s]
	for i := range buf {
		buf[i] = 0
	}
	cols, outs := ob.d.Cols(), ob.h.outputs()
	nw := outs * cols
	sc := &ob.scratch[s]
	dextra := buf[nw+outs : len(buf)-1]
	var loss float64
	from, to := ob.shards[s][0], ob.shards[s][1]
	for _, e := range ob.ex[from:to] {
		scores(ob.d, ob.eff, outs, e.Row, sc.z)
		loss += e.Weight * ob.h.loss(sc.z, ob.extra, e.Target, sc.dz, dextra, e.Weight)
		emb, scale := ob.d.emb(e.Row)
		for k := range outs {
			sc.r[k] = e.Weight * sc.dz[k]
			buf[nw+k] += sc.r[k]
		}
		for j, v := range emb {
			x := float64(v) * scale
			g := buf[j*outs : (j+1)*outs]
			for k := range g {
				g[k] += x * sc.r[k]
			}
		}
		for _, c := range ob.d.cellsOf(e.Row) {
			g := buf[(ob.d.Dims+int(c))*outs : (ob.d.Dims+int(c)+1)*outs]
			for k := range g {
				g[k] += sc.r[k]
			}
		}
	}
	buf[len(buf)-1] = loss
}
