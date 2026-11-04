//go:build !windows

package sidecar

import "fmt"

// platformVulkanProbe is explicit, not a silent success: this build cannot
// check for a Vulkan device, so a Vulkan requirement must fail the check
// (retryable) rather than claim the machine is ready.
func platformVulkanProbe() error {
	return fmt.Errorf("%w: the Vulkan device check is only implemented on Windows", ErrVulkanProbe)
}
