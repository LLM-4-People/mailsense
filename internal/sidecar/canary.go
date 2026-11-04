package sidecar

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/LLM-4-People/mailsense/internal/digest"
	"github.com/LLM-4-People/mailsense/internal/vector"
)

// checkCanary embeds the catalog's canary sentence on the GPU build and
// compares it with the reference: the model's pinned vector, else a cpu
// build's vector of the same sentence. "" means the GPU build may serve;
// otherwise the reason it may not. The probe itself rejects non-finite
// values, a wrong length and a broken norm.
func (in *instance) checkCanary(ctx context.Context, r *running) string {
	in.set(StateCanary, r.build.Variant, nil)
	ref, err := in.reference(ctx, r.build)
	if err != nil {
		return "no canary reference: " + err.Error()
	}
	got, _, err := in.m.probe(ctx, r.build, r.child, []string{r.build.Canary})
	if err != nil {
		return fmt.Sprintf("its canary embedding is unusable: %v", err)
	}
	cos := vector.Cosine(got[0], ref)
	r.canary = cos
	if threshold := in.m.cfg.Float("sidecar.canary.min_cosine"); cos < threshold || len(got[0]) != len(ref) {
		return fmt.Sprintf("canary cosine %.4f with the reference vector is below sidecar.canary.min_cosine %v", cos, threshold)
	}
	in.m.log.Info("GPU canary passed", "runtime", in.runtime, "variant", r.build.Variant, "cosine", cos)
	return ""
}

// reference is the expected canary embedding for gpu: the vector cached
// for this model, sentence and length; else the model's pinned vector
// when it belongs to the current sentence (and embed.dimensions); else a
// vector measured on the platform's cpu build, only when it has one.
// The pinned vector is never regenerated or rescaled.
func (in *instance) reference(ctx context.Context, gpu Build) ([]float32, error) {
	dims := in.m.cfg.Int("embed.dimensions")
	key := strings.Join([]string{gpu.Runtime, gpu.RuntimeSpec.Version, gpu.ModelFile.SHA256, digest.Hex([]byte(gpu.Canary), 12),
		strconv.Itoa(dims)}, "|")
	var v []float32
	var ok bool
	in.m.state.read(func(d *learned) { v, ok = d.Canary[key] })
	if ok {
		return v, nil
	}
	pin, pinErr := pinnedReference(gpu, dims)
	if pin != nil {
		return pin, nil
	}
	if !gpu.RuntimeSpec.Platforms[in.m.platform].hasCPU() {
		if pinErr != nil {
			return nil, fmt.Errorf("%w; a matching pinned canary vector or a cpu build is required (%s has no cpu build)", pinErr, in.m.platform)
		}
		return nil, fmt.Errorf("model %q: a pinned canary vector or a cpu build is required (%s has no cpu build)", gpu.Model, in.m.platform)
	}
	cpu, err := in.m.build(gpu.Runtime, gpu.Model, VariantCPU)
	if err != nil {
		return nil, err
	}
	r, err := in.launchBuild(ctx, cpu)
	if err != nil {
		return nil, err
	}
	defer in.stopChild(r.child)
	in.set(StateCanary, "cpu reference", nil)
	got, _, err := in.m.probe(ctx, cpu, r.child, []string{cpu.Canary})
	if err != nil {
		return nil, err
	}
	if err := in.m.state.update(func(d *learned) { d.Canary[key] = got[0] }); err != nil {
		in.m.log.Warn("canary reference not saved", "err", err)
	}
	return got[0], nil
}

// pinnedReference is the model's pinned vector when it belongs to the
// current canary sentence and length; otherwise nil and, when a vector is
// present but unusable, why.
func pinnedReference(gpu Build, dims int) ([]float32, error) {
	m := gpu.ModelFile
	if len(m.CanaryVector) == 0 {
		return nil, nil
	}
	if m.CanarySentenceSHA256 != digest.Hex([]byte(gpu.Canary), 0) {
		return nil, fmt.Errorf("model %q: canary_sentence_sha256 does not match the current canary sentence", gpu.Model)
	}
	if dims > 0 && len(m.CanaryVector) != dims {
		return nil, fmt.Errorf("model %q: the pinned canary vector has %d values but embed.dimensions is %d", gpu.Model, len(m.CanaryVector), dims)
	}
	return m.CanaryVector, nil
}
