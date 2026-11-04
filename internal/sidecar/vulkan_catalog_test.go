package sidecar

import (
	"encoding/json"
	"strings"
	"testing"
)

// windowsRequires is the embedded catalog with the Windows Vulkan
// variant's requires replaced (omit deletes the field), passed through the
// same overlay merge a sidecar.runtimes.file uses.
func windowsRequires(t *testing.T, value any, omit bool) (*Catalog, error) {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(runtimesJSON, &doc); err != nil {
		t.Fatal(err)
	}
	rt := doc["runtimes"].(map[string]any)["llama-server"].(map[string]any)
	plat := rt["platforms"].(map[string]any)["windows/amd64"].(map[string]any)
	v := plat["variants"].(map[string]any)["vulkan"].(map[string]any)
	if omit {
		delete(v, "requires")
	} else {
		v["requires"] = value
	}
	raw, err := json.Marshal(map[string]any{"runtimes": map[string]any{"llama-server": rt}})
	if err != nil {
		t.Fatal(err)
	}
	return overlaid(t, raw)
}

// The shipped Windows build declares the preflight, and a custom catalog
// may declare it too; an empty or omitted value needs nothing.
func TestCatalogRequiresVulkan(t *testing.T) {
	c, err := parseCatalog(runtimesJSON)
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.DefaultBuild(rtName, "windows/amd64", modelName)
	if err != nil {
		t.Fatal(err)
	}
	if b.Spec.Requires != RequirementVulkan {
		t.Fatalf("the Windows default build requires %q, want %q", b.Spec.Requires, RequirementVulkan)
	}
	for name, tc := range map[string]struct {
		value any
		omit  bool
		want  string
	}{
		"vulkan":  {value: RequirementVulkan, want: RequirementVulkan},
		"empty":   {value: "", want: ""},
		"omitted": {omit: true, want: ""},
	} {
		t.Run(name, func(t *testing.T) {
			c, err := windowsRequires(t, tc.value, tc.omit)
			if err != nil {
				t.Fatal(err)
			}
			b, err := c.DefaultBuild(rtName, "windows/amd64", modelName)
			if err != nil {
				t.Fatal(err)
			}
			if b.Spec.Requires != tc.want {
				t.Fatalf("requires = %q, want %q", b.Spec.Requires, tc.want)
			}
		})
	}
	for _, value := range []string{"cuda", "Vulkan", " vulkan", "vulkan "} {
		t.Run("reject "+value, func(t *testing.T) {
			_, err := windowsRequires(t, value, false)
			if err == nil || !strings.Contains(err.Error(), "unknown requires") ||
				!strings.Contains(err.Error(), `"vulkan"`) || !strings.Contains(err.Error(), value) {
				t.Fatalf("requires %q: %v", value, err)
			}
		})
	}
}
