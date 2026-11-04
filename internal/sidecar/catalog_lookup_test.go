package sidecar

import (
	"errors"
	"strings"
	"testing"
)

// TestPlatformOfIsOneUnsupportedClassification: an unknown runtime and a
// runtime without builds for this machine are the same ErrUnsupported answer,
// from the lookup itself and from both catalog entry points that used to
// spell it. The messages are compared verbatim, so a caller cannot drift.
func TestPlatformOfIsOneUnsupportedClassification(t *testing.T) {
	c, err := parseCatalog(runtimesJSON)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, runtime, platform, want string
	}{
		{"unknown runtime", "nope", "linux/amd64", `unknown runtime "nope"`},
		{"platform without builds", rtName, "plan9/386", "has no builds for plan9/386"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, lookupErr := c.platformOf(tc.runtime, tc.platform)
			if !errors.Is(lookupErr, ErrUnsupported) || !strings.Contains(lookupErr.Error(), tc.want) {
				t.Fatalf("platformOf = %v, want ErrUnsupported containing %q", lookupErr, tc.want)
			}
			if _, err := c.Resolve(tc.runtime, VariantCPU, tc.platform, modelName); err == nil || err.Error() != lookupErr.Error() {
				t.Fatalf("Resolve = %v, the lookup answers %v", err, lookupErr)
			}
			if _, err := c.DefaultBuild(tc.runtime, tc.platform, modelName); err == nil || err.Error() != lookupErr.Error() {
				t.Fatalf("DefaultBuild = %v, the lookup answers %v", err, lookupErr)
			}
		})
	}

	rt, plat, err := c.platformOf(rtName, "linux/amd64")
	if err != nil {
		t.Fatalf("a supported pair: %v", err)
	}
	if rt.Version == "" || !plat.hasCPU() {
		t.Fatalf("platformOf(linux) = %+v %+v", rt, plat)
	}
}

// TestPlatformHasCPUIsTheOneFact: the cpu build decides the fallback, the
// default variant and the validation rule, and all of them read the same
// predicate.
func TestPlatformHasCPUIsTheOneFact(t *testing.T) {
	c, err := parseCatalog(runtimesJSON)
	if err != nil {
		t.Fatal(err)
	}
	linux := c.Runtimes[rtName].Platforms["linux/amd64"]
	windows := c.Runtimes[rtName].Platforms["windows/amd64"]
	if !linux.hasCPU() {
		t.Fatal("the linux platform ships cpu and hasCPU said no")
	}
	if windows.hasCPU() {
		t.Fatal("the windows platform ships vulkan only and hasCPU said yes")
	}

	// The default variant follows the same fact: cpu where it exists, the
	// first auto entry where it does not.
	if v, err := linux.defaultVariant(); err != nil || v != VariantCPU {
		t.Fatalf("linux default = %q %v", v, err)
	}
	if v, err := windows.defaultVariant(); err != nil || v != "vulkan" {
		t.Fatalf("windows default = %q %v", v, err)
	}

	// The validation rule reads it too: auto with no GPU list needs cpu, so a
	// cpu-less platform with no auto list is the problem and one with cpu is
	// not.
	cpuLess := &Platform{Variants: map[string]Variant{"vulkan": {GPU: true}}}
	if got := strings.Join(cpuLess.problems("p"), "\n"); !strings.Contains(got, "a cpu variant is required") {
		t.Fatalf("a cpu-less platform with no auto list passed validation: %q", got)
	}
	withCPU := &Platform{Variants: map[string]Variant{VariantCPU: {}}}
	if got := strings.Join(withCPU.problems("p"), "\n"); strings.Contains(got, "a cpu variant is required") {
		t.Fatalf("a platform with cpu was told it needs one: %q", got)
	}
}
