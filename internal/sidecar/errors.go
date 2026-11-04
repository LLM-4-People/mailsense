// Package sidecar runs local inference servers (llama.cpp's llama-server)
// as managed child processes: pinned artifacts downloaded on demand
// (resumable, cancellable, SHA-256 checked, installed atomically, hashed
// again before every launch), a build chosen per settings (Windows: the
// single Vulkan build; Linux dev/tests: cpu; custom catalogs may list
// more, chosen by auto or by name, with the model's pinned canary vector
// or the platform's cpu build as reference), loopback only on a free port
// with a random key, health supervision with backoff restarts, a thread
// count measured at the first cpu start, and a child that cannot outlive
// the app (a Windows job object with KILL_ON_JOB_CLOSE; the
// parent-death signal on Linux). It is the engine.Locator of managed
// embedders.
package sidecar

import (
	"errors"
	"fmt"

	"github.com/LLM-4-People/mailsense/internal/engine"
)

// Error classes; each wraps engine.ErrUnavailable where retrying the same
// request cannot help until something changes.
var (
	ErrUnsupported  = fmt.Errorf("sidecar: no build for this machine (%w)", engine.ErrUnavailable)
	ErrNotInstalled = fmt.Errorf("sidecar: runtime not installed and sidecar.auto_install is off (%w)", engine.ErrUnavailable)
	ErrStarting     = fmt.Errorf("sidecar: runtime not ready yet (%w)", engine.ErrUnavailable)
	ErrFailed       = fmt.Errorf("sidecar: runtime failed (%w)", engine.ErrUnavailable)
	// ErrNoVulkan is a fact about this machine: no Vulkan driver, or no
	// physical device. Nothing is downloaded and retrying cannot help until
	// a driver is installed (the re-probe action tries again).
	ErrNoVulkan = fmt.Errorf("sidecar: no usable Vulkan device on this machine (install a GPU driver with Vulkan support, or use another machine) (%w)", engine.ErrUnavailable)
	// ErrVulkanProbe means the check itself failed (the loader is present
	// but unusable, a call returned an unexpected result): it may be
	// temporary, so the supervisor may retry it.
	ErrVulkanProbe = fmt.Errorf("sidecar: the Vulkan device check failed (this may be temporary: retry, or re-probe the runtime) (%w)", engine.ErrUnavailable)
	ErrHash        = errors.New("sidecar: artifact does not match its pinned SHA-256")
	ErrStopped     = errors.New("sidecar: manager stopped")
)
