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

// Start runs outlook.exe through the shell (its App Paths registration),
// exactly as the user starting it: its own window, the default profile.
// Never CoCreateInstance: that starts a windowless Outlook.
func (h *winHost) Start() error {
	verb, _ := windows.UTF16PtrFromString("open")
	file, _ := windows.UTF16PtrFromString(outlookExe)
	if err := windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL); err != nil {
		return fmt.Errorf("ShellExecute %s: %w", outlookExe, err)
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
