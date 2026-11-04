package api

import (
	"fmt"
	"strings"
	"testing"

	"github.com/LLM-4-People/mailsense/internal/sidecar"
)

// Round 25: the sidecar's typed preflight errors reach the client before
// the generic unavailable classes, and a status read discloses the
// unavailable build without touching the network or starting a job.
func TestSidecarInstallVulkanErrorCodes(t *testing.T) {
	cases := map[string]struct {
		err    error
		status int
		code   string
	}{
		"no device": {sidecar.ErrNoVulkan, 422, "vulkan_unavailable"},
		"probe":     {fmt.Errorf("install: %w", sidecar.ErrVulkanProbe), 503, "vulkan_probe_failed"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv, ts, w := newTestServer(t)
			w.sidecar.installErr = tc.err
			status, _, data := do(t, ts, srv, request{method: "POST", path: "/api/sidecar/install",
				body: `{"runtime":"llama-server","model":"embeddinggemma-2","variant":"vulkan"}`})
			mustStatus(t, status, tc.status, data)
			if code := errorCode(t, data); code != tc.code {
				t.Fatalf("code %q, want %q: %s", code, tc.code, data)
			}
			if w.sidecar.installCalls != 1 {
				t.Fatalf("install calls = %d, want 1", w.sidecar.installCalls)
			}
		})
	}
}

// The supervisor wraps a preflight failure in ErrFailed before a prompt or
// index caller sees it; the typed cause must still win over the generic
// runtime-unavailable class.
func TestVulkanErrorsSurviveTheSupervisorWrapper(t *testing.T) {
	srv, _, _ := newTestServer(t)
	cases := map[string]struct {
		err    error
		status int
		code   string
	}{
		"no device": {fmt.Errorf("%w: llama-server: %w", sidecar.ErrFailed, sidecar.ErrNoVulkan), 422, "vulkan_unavailable"},
		"probe":     {fmt.Errorf("%w: llama-server: %w", sidecar.ErrFailed, sidecar.ErrVulkanProbe), 503, "vulkan_probe_failed"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			mapped := srv.mapError(tc.err)
			if mapped.Status != tc.status || mapped.Code != tc.code {
				t.Fatalf("mapped %d %s, want %d %s", mapped.Status, mapped.Code, tc.status, tc.code)
			}
		})
	}
}

func TestSidecarStatusReportsUnavailableBuild(t *testing.T) {
	srv, ts, w := newTestServer(t)
	w.sidecar.status = SidecarStatus{Builds: []sidecar.BuildInfo{{
		Runtime: "llama-server", Version: "b11617", Variant: "vulkan", GPU: true, Auto: true,
		Download: 342_039_075, Unavailable: sidecar.RequirementVulkan,
	}}}
	status, _, data := do(t, ts, srv, request{method: "GET", path: "/api/sidecar"})
	mustStatus(t, status, 200, data)
	if !strings.Contains(string(data), `"unavailable":"vulkan"`) {
		t.Fatalf("the status omits the unavailable build: %s", data)
	}
	body := decodeMap(t, data)
	builds, _ := body["builds"].([]any)
	if len(builds) != 1 {
		t.Fatalf("builds: %s", data)
	}
	build, _ := builds[0].(map[string]any)
	if build["unavailable"] != "vulkan" || build["download_bytes"] != float64(342_039_075) {
		t.Fatalf("build: %v", build)
	}
}
