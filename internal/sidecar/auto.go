package sidecar

import (
	"context"
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/mail"
)

// choiceKey identifies an auto choice: runtime, version, model file,
// platform, CPU count and the pins of every candidate (a new pin is
// probed again). cpu is a candidate only when the platform has it.
func (in *instance) choiceKey() (string, error) {
	cat, err := LoadCatalog(in.m.cfg)
	if err != nil {
		return "", err
	}
	rt, plat, err := cat.platformOf(in.runtime, in.m.platform)
	if err != nil {
		return "", err
	}
	names := append([]string(nil), plat.Auto...)
	if plat.hasCPU() {
		names = append([]string{VariantCPU}, names...)
	}
	var all []string
	for _, v := range names {
		b := Build{Spec: plat.Variants[v]}
		all = append(all, v+"="+strings.Join(pins(b), ","))
	}
	model := cat.Models[in.model].SHA256
	return strings.Join([]string{in.runtime, rt.Version, model, in.m.platform, strconv.Itoa(runtime.NumCPU()), digest.Hex([]byte(strings.Join(all, ";")), 12)}, "|"), nil
}

// autoChoice is the persisted choice, or a new one: every candidate of the
// platform's auto list is installed, started and checked with the canary.
// When the platform has a cpu build, a GPU build wins only when it is
// correct AND at least sidecar.auto.min_speedup times the cpu build's
// rate (an integrated GPU passes the canary but is slower: it never
// wins). Without a cpu build the first candidate that passes the canary
// serves: there is no rate to compare and no fallback.
func (in *instance) autoChoice(ctx context.Context) (Choice, error) {
	key, err := in.choiceKey()
	if err != nil {
		return Choice{}, err
	}
	var c Choice
	var ok bool
	in.m.state.read(func(d *learned) { c, ok = d.Choices[key] })
	if ok {
		return c, nil
	}
	if c, err = in.probeAuto(ctx); err != nil {
		return Choice{}, err
	}
	if err := in.m.state.update(func(d *learned) { d.Choices[key] = c }); err != nil {
		in.m.log.Warn("auto choice not saved", "err", err)
	}
	attrs := []any{"runtime", in.runtime, "variant", c.Variant, "reason", c.Reason}
	if c.CPU > 0 {
		attrs = append(attrs, "cpu_tokens_per_second", c.CPU)
	}
	in.m.log.Info("auto build chosen", attrs...)
	return c, nil
}

func (in *instance) probeAuto(ctx context.Context) (Choice, error) {
	now := in.m.clock.Now().UTC().Format(mail.TimeLayout)
	cat, err := LoadCatalog(in.m.cfg)
	if err != nil {
		return Choice{}, err
	}
	_, plat, err := cat.platformOf(in.runtime, in.m.platform)
	if err != nil {
		return Choice{}, err
	}
	auto := plat.Auto
	if len(auto) == 0 {
		variant, err := plat.defaultVariant()
		if err != nil {
			return Choice{}, err
		}
		return Choice{Variant: variant, Reason: "the platform lists no GPU build to try", DecidedAt: now}, nil
	}
	if !plat.hasCPU() {
		return in.probeAutoWithoutCPU(ctx, auto)
	}
	cpu, err := in.m.build(in.runtime, in.model, VariantCPU)
	if err != nil {
		return Choice{}, err
	}
	rate, err := in.cpuRate(ctx, cpu)
	if err != nil {
		return Choice{}, err
	}
	c := Choice{Variant: VariantCPU, CPU: rate, Reason: "no build of the auto list beat cpu"}
	best, need := rate, in.m.cfg.Float("sidecar.auto.min_speedup")
	for _, v := range auto {
		p, ok, requirementErr := in.probeBuild(ctx, v)
		if ctx.Err() != nil {
			return Choice{}, ctx.Err()
		}
		// A GPU candidate this machine cannot run does not fail the choice:
		// the measured cpu build still serves.
		if requirementErr == nil && ok {
			p.Speedup = p.TokensPerSecond / rate
			switch {
			case p.Speedup < need:
				p.Error = fmt.Sprintf("only %.2f× the cpu rate (sidecar.auto.min_speedup %v)", p.Speedup, need)
			case p.TokensPerSecond > best:
				best, c.Variant = p.TokensPerSecond, v
				c.Reason = fmt.Sprintf("%s passed the canary (%.4f) at %.2f× the cpu rate", v, p.Canary, p.Speedup)
			}
		}
		c.Probes = append(c.Probes, p)
	}
	c.DecidedAt = in.m.clock.Now().UTC().Format(mail.TimeLayout) // after the probes
	return c, nil
}

// probeAutoWithoutCPU tries the auto list on a platform with no cpu
// build: each candidate is installed, started and canary-checked, and
// the first one that passes serves. No cpu rate is measured and
// sidecar.auto.min_speedup does not apply.
func (in *instance) probeAutoWithoutCPU(ctx context.Context, auto []string) (Choice, error) {
	var failures []string
	var probes []Probe
	for _, v := range auto {
		p, ok, requirementErr := in.probeBuild(ctx, v)
		if ctx.Err() != nil {
			return Choice{}, ctx.Err()
		}
		probes = append(probes, p)
		// Without a cpu build there is nothing else to try: keep the typed
		// requirement error so errors.Is survives to the API.
		if requirementErr != nil {
			return Choice{}, fmt.Errorf("sidecar: the %s build of %s cannot run on %s (no cpu build): %w", v, in.runtime, in.m.platform, requirementErr)
		}
		if !ok {
			if p.Error != "" {
				failures = append(failures, v+": "+p.Error)
			}
			continue
		}
		return Choice{Variant: v, Reason: fmt.Sprintf("%s passed the canary (%.4f)", v, p.Canary),
			Probes: probes, DecidedAt: in.m.clock.Now().UTC().Format(mail.TimeLayout)}, nil
	}
	return Choice{}, fmt.Errorf("sidecar: no %s build of %s passed the canary (%s has no cpu build): %s",
		in.m.platform, in.runtime, in.m.platform, strings.Join(failures, "; "))
}

// probeBuild tries one auto candidate: start, canary and throughput. ok
// is false when the probe says why it failed. requirementErr is returned
// separately (and the report keeps its message for the UI) so auto can
// keep the typed error instead of stringifying it.
func (in *instance) probeBuild(ctx context.Context, variant string) (Probe, bool, error) {
	p := Probe{Variant: variant, At: in.m.clock.Now().UTC().Format(mail.TimeLayout)}
	b, err := in.m.build(in.runtime, in.model, variant)
	if err != nil {
		p.Error = err.Error()
		return p, false, nil
	}
	if err := in.m.require(b); err != nil {
		p.Error = err.Error()
		return p, false, err
	}
	in.set(StateProbing, variant, nil)
	r, err := in.launchBuild(ctx, b)
	if err != nil {
		p.Error = "did not start: " + err.Error()
		return p, false, nil
	}
	defer in.stopChild(r.child)
	if reason := in.checkCanary(ctx, r); reason != "" {
		p.Canary, p.Error = r.canary, reason
		return p, false, nil
	}
	p.Canary = r.canary
	tps, err := in.m.measure(ctx, b, r.child, tuneTexts(b.TuneText, in.m.cfg.Int("sidecar.tune.inputs")))
	if err != nil {
		p.Error = "not measured: " + err.Error()
		return p, false, nil
	}
	p.TokensPerSecond = tps
	return p, true, nil
}

// cpuRate is the cpu build's tokens per second at its thread count: the
// tuning's measurement, or one measured now (fixed sidecar.threads).
func (in *instance) cpuRate(ctx context.Context, cpu Build) (float64, error) {
	if err := in.m.ensureInstalled(ctx, cpu, func() { in.set(StateInstalling, cpu.Variant, nil) }); err != nil {
		return 0, err
	}
	threads, err := in.threadsFor(ctx, cpu)
	if err != nil {
		return 0, err
	}
	var t Tuning
	var ok bool
	in.m.state.read(func(d *learned) { t, ok = d.Tuning[tuningKey(cpu)] })
	if rate := t.TokensPerSecond[strconv.Itoa(threads)]; ok && rate > 0 {
		return rate, nil
	}
	r, err := in.launchBuild(ctx, cpu)
	if err != nil {
		return 0, err
	}
	defer in.stopChild(r.child)
	return in.m.measure(ctx, cpu, r.child, tuneTexts(cpu.TuneText, in.m.cfg.Int("sidecar.tune.inputs")))
}
