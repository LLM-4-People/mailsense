//go:build windows

package sidecar

import (
	"errors"
	"os"
	"testing"
	"unsafe"
)

// The Vulkan loader reads these structs by their C layout: a wrong size or
// offset is memory corruption, so pin every field the probe touches.
func TestVulkanStructLayout(t *testing.T) {
	if got := unsafe.Sizeof(vkApplicationInfo{}); got != 48 {
		t.Errorf("VkApplicationInfo is %d bytes, want 48", got)
	}
	if got := unsafe.Sizeof(vkInstanceCreateInfo{}); got != 64 {
		t.Errorf("VkInstanceCreateInfo is %d bytes, want 64", got)
	}
	app := vkApplicationInfo{}
	for _, c := range []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"applicationInfo.sType", unsafe.Offsetof(app.sType), 0},
		{"applicationInfo.pNext", unsafe.Offsetof(app.pNext), 8},
		{"applicationInfo.pApplicationName", unsafe.Offsetof(app.pApplicationName), 16},
		{"applicationInfo.applicationVersion", unsafe.Offsetof(app.applicationVersion), 24},
		{"applicationInfo.pEngineName", unsafe.Offsetof(app.pEngineName), 32},
		{"applicationInfo.engineVersion", unsafe.Offsetof(app.engineVersion), 40},
		{"applicationInfo.apiVersion", unsafe.Offsetof(app.apiVersion), 44},
	} {
		if c.got != c.want {
			t.Errorf("%s is at offset %d, want %d", c.name, c.got, c.want)
		}
	}
	info := vkInstanceCreateInfo{}
	for _, c := range []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"instanceCreateInfo.sType", unsafe.Offsetof(info.sType), 0},
		{"instanceCreateInfo.pNext", unsafe.Offsetof(info.pNext), 8},
		{"instanceCreateInfo.flags", unsafe.Offsetof(info.flags), 16},
		{"instanceCreateInfo.pApplicationInfo", unsafe.Offsetof(info.pApplicationInfo), 24},
		{"instanceCreateInfo.enabledLayerCount", unsafe.Offsetof(info.enabledLayerCount), 32},
		{"instanceCreateInfo.ppEnabledLayerNames", unsafe.Offsetof(info.ppEnabledLayerNames), 40},
		{"instanceCreateInfo.enabledExtensionCount", unsafe.Offsetof(info.enabledExtensionCount), 48},
		{"instanceCreateInfo.ppEnabledExtensionNames", unsafe.Offsetof(info.ppEnabledExtensionNames), 56},
	} {
		if c.got != c.want {
			t.Errorf("%s is at offset %d, want %d", c.name, c.got, c.want)
		}
	}
}

// TestVulkanLiveProbe runs the real loader on a Windows machine. It is
// opt-in: MAILSENSE_VULKAN_LIVE=1 and MAILSENSE_VULKAN_EXPECT=present|none
// state what the machine is known to have.
func TestVulkanLiveProbe(t *testing.T) {
	if os.Getenv("MAILSENSE_VULKAN_LIVE") != "1" {
		t.Skip("set MAILSENSE_VULKAN_LIVE=1 on a Windows machine to run the live Vulkan check")
	}
	want := os.Getenv("MAILSENSE_VULKAN_EXPECT")
	if want != "present" && want != "none" {
		t.Fatalf("MAILSENSE_VULKAN_EXPECT must be present or none, got %q", want)
	}
	err := platformVulkanProbe()
	if want == "present" {
		if err != nil {
			t.Fatalf("this machine has a Vulkan device, but the probe failed: %v", err)
		}
		return
	}
	if !errors.Is(err, ErrNoVulkan) || errors.Is(err, ErrVulkanProbe) {
		t.Fatalf("this machine has no Vulkan device, but the probe returned %v", err)
	}
}
