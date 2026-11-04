package sidecar

import (
	"context"
	"fmt"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/mail"
	"github.com/LLM-4-People/mailsense/internal/mathx"
)

// threadsFor is the thread count of a start: sidecar.threads when set;
// otherwise the cpu build's measured best (measured now on the first cpu
// start); a GPU build without a measurement leaves it to the runtime (-1),
// and one on a platform without a cpu build does the same.
func (in *instance) threadsFor(ctx context.Context, b Build) (int, error) {
	if t := in.m.cfg.Int("sidecar.threads"); t > 0 {
		return t, nil
	}
	cpu := b
	if b.Spec.GPU {
		if !in.hasCPU(b) {
			return -1, nil
		}
		var err error
		if cpu, err = in.m.build(b.Runtime, b.Model, VariantCPU); err != nil {
			return 0, err
		}
	}
	var t Tuning
	var ok bool
	in.m.state.read(func(d *learned) { t, ok = d.Tuning[tuningKey(cpu)] })
	if ok {
		return t.Threads, nil
	}
	if b.Spec.GPU {
		return -1, nil
	}
	return in.tune(ctx, b)
}

// tuningKey is the identity of a measurement: build, model and machine.
func tuningKey(b Build) string {
	return strings.Join([]string{b.Runtime, b.RuntimeSpec.Version, b.Variant, b.ModelFile.SHA256, strconv.Itoa(runtime.NumCPU())}, "|")
}

// tune starts the build once per candidate thread count, measures the
// tokens per second of the same distinct texts, and keeps the fastest.
func (in *instance) tune(ctx context.Context, b Build) (int, error) {
	cands := candidates(in.m.cfg.Ints("sidecar.tune.cpu_percent"), runtime.NumCPU())
	in.set(StateTuning, fmt.Sprintf("threads %v", cands), func(s *Status) { s.Variant = b.Variant })
	texts := tuneTexts(b.TuneText, in.m.cfg.Int("sidecar.tune.inputs"))
	res := Tuning{TokensPerSecond: map[string]float64{}}
	best := -1.0
	for _, t := range cands {
		c, err := in.m.launchReady(ctx, b, t)
		if err != nil {
			return 0, err
		}
		tps, err := in.m.measure(ctx, b, c, texts)
		in.stopChild(c)
		if err != nil {
			return 0, fmt.Errorf("sidecar: tuning with %d threads: %w", t, err)
		}
		res.TokensPerSecond[strconv.Itoa(t)] = tps
		in.m.log.Info("thread count measured", "runtime", b.Runtime, "variant", b.Variant, "threads", t, "tokens_per_second", tps)
		if tps > best {
			best, res.Threads = tps, t
		}
	}
	res.MeasuredAt = in.m.clock.Now().UTC().Format(mail.TimeLayout)
	if err := in.m.state.update(func(d *learned) { d.Tuning[tuningKey(b)] = res }); err != nil {
		in.m.log.Warn("thread tuning not saved", "err", err)
	}
	return res.Threads, nil
}

// measure is the throughput of one running build over texts (after one
// warm-up request that pays the graph setup).
func (m *Manager) measure(ctx context.Context, b Build, c *child, texts []string) (float64, error) {
	if _, _, err := m.probe(ctx, b, c, texts[:1]); err != nil {
		return 0, err
	}
	start := m.clock.Now()
	_, tokens, err := m.probe(ctx, b, c, texts[1:])
	if err != nil {
		return 0, err
	}
	secs := m.clock.Now().Sub(start).Seconds()
	if secs <= 0 {
		return 0, fmt.Errorf("no time measured")
	}
	return float64(tokens) / secs, nil
}

// candidates are the thread counts to measure: the given percentages of
// the machine's logical CPUs (rounded half-up, at least one), ascending
// and unique; all of them when no percentage is given.
func candidates(percents []int, cpus int) []int {
	var out []int
	for _, p := range percents {
		t := max(1, int(mathx.Round(float64(cpus*p)/100, 0)))
		if p >= 1 && t <= cpus && !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		out = []int{cpus}
	}
	slices.Sort(out)
	return out
}

// tuneTexts are n distinct texts: the tuning text rotated by a different
// number of words each time, so no prompt cache can reuse a prefix.
func tuneTexts(text string, n int) []string {
	words := mail.Words(text)
	out := make([]string, max(n, 2))
	for i := range out {
		k := i % len(words)
		out[i] = strconv.Itoa(i) + " " + strings.Join(append(append([]string(nil), words[k:]...), words[:k]...), " ")
	}
	return out
}
