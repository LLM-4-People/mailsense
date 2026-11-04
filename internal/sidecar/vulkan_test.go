//go:build !windows

package sidecar

import (
	"errors"
	"testing"
)

// The stub must fail the check explicitly on a platform that cannot run it;
// a nil here would let a Vulkan requirement through as if it were met.
func TestVulkanProbeIsWindowsOnly(t *testing.T) {
	err := platformVulkanProbe()
	if !errors.Is(err, ErrVulkanProbe) || errors.Is(err, ErrNoVulkan) {
		t.Fatalf("the non-Windows probe returned %v, want ErrVulkanProbe only", err)
	}
}
