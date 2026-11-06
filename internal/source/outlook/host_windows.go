//go:build windows

package outlook

import (
	"fmt"

	ole "github.com/go-ole/go-ole"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	progID = "Outlook.Application"
	// The per-user switch to the new Outlook (which has no COM).
	newOutlookKey   = `Software\Microsoft\Office\16.0\Outlook\Preferences`
	newOutlookValue = "UseNewOutlook"
)

// winHost is the real COM host.
type winHost struct{ ids *dispidCache }

func newHost() (host, error) { return &winHost{ids: newDispidCache()}, nil }

func (h *winHost) Init() error {
	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		return comError("CoInitializeEx", err)
	}
	return nil
}

func (h *winHost) Uninit() { ole.CoUninitialize() }

func (h *winHost) Elevated() (bool, error) { return windows.GetCurrentProcessToken().IsElevated(), nil }

func (h *winHost) Registered() error {
	_, err := ole.CLSIDFromProgID(progID)
	if err != nil {
		return comError("CLSIDFromProgID", err)
	}
	return nil
}

func (h *winHost) NewOutlook() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, newOutlookKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue(newOutlookValue)
	return err == nil && v == 1
}

func (h *winHost) Attach() (Object, error) {
	return h.object("GetActiveObject", func(clsid *ole.GUID) (*ole.IUnknown, error) { return ole.GetActiveObject(clsid, ole.IID_IUnknown) })
}

// Start starts classic Outlook through the shell, exactly as the user
// starting it: its own window, the configured profile (the default profile
// when empty). It opens the absolute path its App Paths registration
// resolved to (apppaths.go), never the bare name: the shell would search its
// own path — the working and install directories included — and a planted
// OUTLOOK.EXE there could run instead. Never CoCreateInstance: that starts a
// windowless Outlook.
func (h *winHost) Start(profile string) error {
	return launchOutlook(registryAppPath, openOutlook, profile)
}

// registryAppPath reads the default (unnamed) value of an App Paths
// registration: the absolute path the Outlook installer wrote, per
// machine (HKLM) or per user (HKCU). A missing key or a non-string value
// does not resolve.
func registryAppPath(root appPathRoot, subkey string) (string, bool) {
	hive := registry.CURRENT_USER
	if root == appPathHKLM {
		hive = registry.LOCAL_MACHINE
	}
	k, err := registry.OpenKey(hive, subkey, registry.QUERY_VALUE)
	if err != nil {
		return "", false
	}
	defer k.Close()
	exe, _, err := k.GetStringValue("")
	return exe, err == nil
}

// openOutlook opens exe through the shell ("open"): its own window, with the
// already validated profile parameters (empty for the default profile).
func openOutlook(exe, params string) error {
	verb, _ := windows.UTF16PtrFromString("open")
	file, _ := windows.UTF16PtrFromString(exe)
	var paramsPtr *uint16
	if params != "" {
		paramsPtr, _ = windows.UTF16PtrFromString(params)
	}
	if err := windows.ShellExecute(0, verb, file, paramsPtr, nil, windows.SW_SHOWNORMAL); err != nil {
		return fmt.Errorf("ShellExecute %s: %w", exe, err)
	}
	return nil
}

// object obtains Outlook.Application through get and returns its IDispatch.
func (h *winHost) object(what string, get func(*ole.GUID) (*ole.IUnknown, error)) (Object, error) {
	clsid, err := ole.CLSIDFromProgID(progID)
	if err != nil {
		return nil, comError("CLSIDFromProgID", err)
	}
	unk, err := get(clsid)
	if err != nil {
		return nil, comError(what, err)
	}
	defer unk.Release()
	d, err := unk.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return nil, comError("QueryInterface", err)
	}
	return &dispatch{d: d, ids: h.ids}, nil
}
