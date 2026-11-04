//go:build windows

package desktop

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Open opens a validated loopback URL in the owner's default browser. It is
// the fallback when the WebView2 window cannot start: the Windows exe must
// never leave a headless process with no visible surface. ShellExecuteW
// returns a value above 32 on success; anything else is a failure.
func Open(raw string) error {
	target, err := localURL(raw)
	if err != nil {
		return err
	}
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	url, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	shell32 := windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteW := shell32.NewProc("ShellExecuteW")
	ret, _, _ := procShellExecuteW.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(url)), 0, 0, 1 /* SW_SHOWNORMAL */)
	if ret <= 32 {
		return errors.New("desktop: the default browser could not be opened")
	}
	return nil
}
