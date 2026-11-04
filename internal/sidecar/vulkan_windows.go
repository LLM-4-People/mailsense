//go:build windows

package sidecar

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The Vulkan loader's ABI for the three calls the preflight needs. The
// layouts are the 64-bit ones (amd64 and arm64); vulkan_windows_test.go
// pins every size and offset.
const (
	vkSuccess                          = 0
	vkIncomplete                       = 5
	vkErrorInitializationFailed        = -3
	vkErrorIncompatibleDriver          = -9
	vkStructureTypeAppInfo             = 0
	vkStructureTypeCreateInfo          = 1
	vkApiVersion1_0             uint32 = 1 << 22
)

type vkApplicationInfo struct {
	sType              int32
	pNext              unsafe.Pointer
	pApplicationName   *byte
	applicationVersion uint32
	pEngineName        *byte
	engineVersion      uint32
	apiVersion         uint32
}

type vkInstanceCreateInfo struct {
	sType                   int32
	pNext                   unsafe.Pointer
	flags                   uint32
	pApplicationInfo        *vkApplicationInfo
	enabledLayerCount       uint32
	ppEnabledLayerNames     **byte
	enabledExtensionCount   uint32
	ppEnabledExtensionNames **byte
}

// vulkanDLL is loaded from System32 only: a vulkan-1.dll next to the app
// or on the search path can never be picked up.
var vulkanDLL = windows.NewLazySystemDLL("vulkan-1.dll")

// platformVulkanProbe creates a Vulkan instance, enumerates the physical
// devices and destroys the instance. It never starts or downloads a
// runtime; a machine that cannot do this cannot run the Vulkan build.
func platformVulkanProbe() error {
	if err := vulkanDLL.Load(); err != nil {
		if missingVulkanModule(err) {
			return fmt.Errorf("%w: vulkan-1.dll was not found", ErrNoVulkan)
		}
		return fmt.Errorf("%w: loading vulkan-1.dll: %v", ErrVulkanProbe, err)
	}
	create := vulkanDLL.NewProc("vkCreateInstance")
	enumerate := vulkanDLL.NewProc("vkEnumeratePhysicalDevices")
	destroy := vulkanDLL.NewProc("vkDestroyInstance")
	for _, proc := range []*windows.LazyProc{create, enumerate, destroy} {
		if err := proc.Find(); err != nil {
			return fmt.Errorf("%w: vulkan-1.dll has no %s: %v", ErrVulkanProbe, proc.Name, err)
		}
	}

	app := vkApplicationInfo{sType: vkStructureTypeAppInfo, applicationVersion: vkApiVersion1_0, apiVersion: vkApiVersion1_0}
	info := vkInstanceCreateInfo{sType: vkStructureTypeCreateInfo, pApplicationInfo: &app}
	var instance uintptr
	res, _, _ := create.Call(uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&instance)))
	runtime.KeepAlive(&app)
	runtime.KeepAlive(&info)
	switch int32(res) {
	case vkSuccess:
	case vkErrorIncompatibleDriver, vkErrorInitializationFailed:
		return fmt.Errorf("%w: the Vulkan loader found no compatible driver (VkResult %d)", ErrNoVulkan, int32(res))
	default:
		return fmt.Errorf("%w: vkCreateInstance returned VkResult %d", ErrVulkanProbe, int32(res))
	}
	if instance == 0 {
		return fmt.Errorf("%w: vkCreateInstance returned no instance", ErrVulkanProbe)
	}
	defer destroy.Call(instance, 0)
	return enumerateVulkanDevices(enumerate, instance)
}

func enumerateVulkanDevices(enumerate *windows.LazyProc, instance uintptr) error {
	var count uint32
	res, _, _ := enumerate.Call(instance, uintptr(unsafe.Pointer(&count)), 0)
	switch int32(res) {
	case vkSuccess:
	case vkIncomplete:
		if count > 0 {
			return nil
		}
		return fmt.Errorf("%w: Vulkan reported no physical device", ErrNoVulkan)
	default:
		return fmt.Errorf("%w: vkEnumeratePhysicalDevices returned VkResult %d", ErrVulkanProbe, int32(res))
	}
	if count == 0 {
		return fmt.Errorf("%w: Vulkan found no physical device", ErrNoVulkan)
	}
	devices := make([]uintptr, count)
	res, _, _ = enumerate.Call(instance, uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&devices[0])))
	runtime.KeepAlive(devices)
	switch int32(res) {
	case vkSuccess:
		if count == 0 {
			return fmt.Errorf("%w: Vulkan found no physical device", ErrNoVulkan)
		}
		return nil
	case vkIncomplete:
		// The list was truncated, but at least one device was written.
		if count > 0 {
			return nil
		}
		return fmt.Errorf("%w: Vulkan found no physical device", ErrNoVulkan)
	default:
		return fmt.Errorf("%w: vkEnumeratePhysicalDevices returned VkResult %d", ErrVulkanProbe, int32(res))
	}
}

// missingVulkanModule separates "this machine has no Vulkan loader" from a
// present-but-unusable one (access denied, wrong architecture).
func missingVulkanModule(err error) bool {
	return errors.Is(err, windows.ERROR_MOD_NOT_FOUND) || errors.Is(err, windows.ERROR_FILE_NOT_FOUND) ||
		errors.Is(err, windows.ERROR_PATH_NOT_FOUND)
}
