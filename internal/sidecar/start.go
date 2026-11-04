package sidecar

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/engine"
	"github.com/LLM-4-People/mailsense/internal/mail"
)

// running is a started, healthy runtime.
type running struct {
	build     Build
	child     *child
	threads   int
	requested string  // sidecar.variant when started
	fallback  string  // why the build running is not the one asked for
	canary    float64 // GPU canary cosine
}

// start brings up the build sidecar.variant asks for: auto's persisted
// choice (probed now when there is none), or the named build unless it
// is marked broken. A GPU build that does not start or fails the canary
// is marked broken; it is replaced by cpu only when the platform has a
// cpu build, otherwise the error is returned (no hidden fallback).
func (in *instance) start(ctx context.Context) (*running, error) {
	requested := in.m.cfg.String("sidecar.variant")
	variant, why, err := in.choose(ctx, requested)
	if err != nil {
		return nil, err
	}
	b, err := in.m.build(in.runtime, in.model, variant)
	if err != nil {
		return nil, err
	}
	r, err := in.launchBuild(ctx, b)
	// A missing driver or a failed device check is a fact about this
	// machine, not a broken build: return it before the canary, the broken
	// mark and any fallback.
	if errors.Is(err, ErrNoVulkan) || errors.Is(err, ErrVulkanProbe) {
		return nil, err
	}
	if err == nil && b.Spec.GPU {
		if reason := in.checkCanary(ctx, r); reason != "" {
			in.stopChild(r.child)
			r, err = nil, fmt.Errorf("%s", reason)
		}
	}
	if err == nil {
		r.requested, r.fallback = requested, why
		return r, nil
	}
	if !b.Spec.GPU || ctx.Err() != nil {
		return nil, err
	}
	in.markBroken(b, requested, err.Error())
	if !in.hasCPU(b) {
		return nil, fmt.Errorf("the %s build is not used: %w (this platform has no cpu build: re-probe the runtime to try again)", variant, err)
	}
	return in.fallBack(ctx, requested, fmt.Sprintf("the %s build is not used: %v", variant, err))
}

// choose is the variant to start and, when it is cpu instead of what was
// asked for, why. A broken build is never tried again; without a cpu
// build the explicit re-probe error is returned instead.
func (in *instance) choose(ctx context.Context, requested string) (string, string, error) {
	if requested != VariantAuto {
		b, err := in.m.build(in.runtime, in.model, requested)
		if err != nil {
			return "", "", err
		}
		if p, broken := in.broken(b); broken {
			return in.brokenChoice(b, p)
		}
		return requested, "", nil
	}
	c, err := in.autoChoice(ctx)
	if err != nil {
		return "", "", err
	}
	if c.Variant == VariantCPU {
		b, err := in.m.build(in.runtime, in.model, VariantCPU)
		if err != nil || !in.hasCPU(b) {
			return "", "", fmt.Errorf("sidecar: auto chose cpu but %s has no cpu build (re-probe the runtime to try again)", in.m.platform)
		}
		return VariantCPU, "auto: " + c.Reason, nil
	}
	b, err := in.m.build(in.runtime, in.model, c.Variant)
	if err != nil {
		return "", "", err
	}
	if p, broken := in.broken(b); broken {
		return in.brokenChoice(b, p)
	}
	return c.Variant, "", nil
}

// brokenChoice replaces a broken build by cpu only when the platform has
// one; otherwise it reports the build with the re-probe hint.
func (in *instance) brokenChoice(b Build, p Probe) (string, string, error) {
	if !in.hasCPU(b) {
		return "", "", fmt.Errorf("sidecar: the %s build is marked broken (%s) and %s has no cpu build: re-probe the runtime to try again",
			b.Variant, p.Error, in.m.platform)
	}
	return VariantCPU, fmt.Sprintf("the %s build is not used: %s (re-probe to try again)", b.Variant, p.Error), nil
}

// hasCPU reports whether the build's runtime has a cpu build on this
// platform (the canary measure and the only fallback). The fact belongs to
// the platform.
func (in *instance) hasCPU(b Build) bool {
	return b.RuntimeSpec.Platforms[in.m.platform].hasCPU()
}

func (in *instance) fallBack(ctx context.Context, requested, reason string) (*running, error) {
	in.m.log.Warn("GPU build replaced by cpu", "runtime", in.runtime, "reason", reason)
	cpu, err := in.m.build(in.runtime, in.model, VariantCPU)
	if err != nil {
		return nil, err
	}
	r, err := in.launchBuild(ctx, cpu)
	if err != nil {
		return nil, err
	}
	r.requested, r.fallback = requested, reason
	return r, nil
}

// brokenKey identifies a build's pins on this machine's model.
func brokenKey(b Build) string {
	return strings.Join([]string{b.Runtime, b.RuntimeSpec.Version, b.Variant, digest.Hex([]byte(strings.Join(pins(b), ",")), 12)}, "|")
}

func (in *instance) broken(b Build) (Probe, bool) {
	var p Probe
	var ok bool
	in.m.state.read(func(d *learned) { p, ok = d.Broken[brokenKey(b)] })
	return p, ok
}

// markBroken persists why a GPU build must not be used. Under auto, a
// cpu choice replaces it only when the platform has a cpu build;
// otherwise the choice keeps the broken variant so the next start
// reports the re-probe error instead of falling back.
func (in *instance) markBroken(b Build, requested, reason string) {
	p := Probe{Variant: b.Variant, Error: reason, At: in.m.clock.Now().UTC().Format(mail.TimeLayout)}
	key, keyErr := in.choiceKey()
	err := in.m.state.update(func(d *learned) {
		d.Broken[brokenKey(b)] = p
		c, ok := d.Choices[key]
		if !ok || keyErr != nil || requested != VariantAuto || c.Variant != b.Variant {
			return
		}
		if in.hasCPU(b) {
			c.Variant, c.Reason = VariantCPU, fmt.Sprintf("the %s build broke after it was chosen: %s", b.Variant, reason)
		} else {
			c.Reason = fmt.Sprintf("the %s build broke after it was chosen: %s (re-probe to try again)", b.Variant, reason)
		}
		c.Probes = append(c.Probes, p)
		d.Choices[key] = c
	})
	if err != nil {
		in.m.log.Warn("broken build not saved", "err", err)
	}
	in.m.log.Warn("GPU build marked broken", "runtime", b.Runtime, "variant", b.Variant, "reason", reason)
}

// launchBuild installs (if needed and allowed), verifies, picks the thread
// count (tuning it at the first cpu start) and starts the build.
func (in *instance) launchBuild(ctx context.Context, b Build) (*running, error) {
	installing := func() { in.set(StateInstalling, b.Variant, func(s *Status) { s.Variant = b.Variant }) }
	if err := in.m.ensureInstalled(ctx, b, installing); err != nil {
		return nil, err
	}
	threads, err := in.threadsFor(ctx, b)
	if err != nil {
		return nil, err
	}
	in.set(StateStarting, b.Variant, func(s *Status) { s.Variant, s.Threads = b.Variant, threads })
	c, err := in.m.launchReady(ctx, b, threads)
	if err != nil {
		return nil, err
	}
	return &running{build: b, child: c, threads: threads}, nil
}

// launchAttempts bounds the port-collision retry of one launch. freePort
// closes its probe listener before the runtime binds, so another program can
// take the port in between and the runtime then exits without ever answering.
// That is a fact about this machine at that instant, not a property of the
// build, so the launch asks for another port rather than marking a working
// build broken. A build that fails for its own reason still fails on the
// first attempt: once the child is gone nothing holds its port.
const launchAttempts = 3

// launchReady starts a build and waits for /health within
// sidecar.start_timeout, then checks the child itself holds its port
// (the process output is quoted when it fails). The child's WaitDelay is
// the stop grace: after a stop it bounds the stdout/stderr copy wait when
// a grandchild inherited the pipes. A restart still waits for the process
// to die (child.stop's normal path), so the next runtime cannot race the
// old one for its port.
func (m *Manager) launchReady(ctx context.Context, b Build, threads int) (*child, error) {
	rt := b.RuntimeSpec
	stopGrace := m.cfg.Duration("sidecar.stop_timeout")
	var last *child
	var lastErr error
	for attempt := 1; attempt <= launchAttempts; attempt++ {
		c, err := launch(launchSpec{exe: m.layout.exe(b), model: m.layout.model(b), args: append(append([]string(nil), rt.Args...), b.Spec.Args...),
			env: b.Spec.Env, envAllow: rt.EnvAllow, keyEnv: rt.KeyEnv, key: m.key, threads: threads, slots: rt.Slots, slotCtx: rt.SlotContext,
			waitDelay: stopGrace},
			m.log.With("runtime", b.Runtime, "variant", b.Variant))
		if err != nil {
			if last != nil {
				last.stop(stopGrace, m.clock)
			}
			return nil, err
		}
		if err := m.waitReady(ctx, c, b); err != nil {
			c.stop(stopGrace, m.clock)
			last, lastErr = c, err
			taken, perr := portHeld(c.port)
			if attempt == launchAttempts || perr != nil || !taken || ctx.Err() != nil {
				break
			}
			m.log.Warn("runtime port taken by another process; retrying with a new one",
				"runtime", b.Runtime, "variant", b.Variant, "port", c.port, "attempt", attempt)
			continue
		}
		return c, nil
	}
	return nil, fmt.Errorf("sidecar: %s %s did not start: %w; output: %s", b.Runtime, b.Variant, lastErr, last.out.last(5))
}

func (m *Manager) waitReady(ctx context.Context, c *child, b Build) error {
	deadline := m.clock.Now().Add(m.cfg.Duration("sidecar.start_timeout"))
	for {
		if c.exited() {
			return fmt.Errorf("the process exited (%v)", c.err)
		}
		if m.healthy(ctx, c, b) == nil {
			if err := c.owned(); err != nil {
				return err
			}
			if c.exited() {
				return fmt.Errorf("the process exited right after answering (%v)", c.err)
			}
			return nil
		}
		if !m.clock.Now().Before(deadline) {
			return fmt.Errorf("no healthy answer within sidecar.start_timeout (%s)", m.cfg.Duration("sidecar.start_timeout"))
		}
		if err := m.clock.Sleep(ctx, m.cfg.Duration("sidecar.start_poll")); err != nil {
			return err
		}
	}
}

func endpointOf(c *child) engine.Endpoint { return engine.Endpoint{URL: c.url, Key: c.key} }
