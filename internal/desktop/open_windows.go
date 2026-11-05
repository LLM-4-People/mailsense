//go:build windows

package desktop

import (
	"errors"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Open opens a validated loopback URL in the owner's default browser. It is
// the fallback when the WebView2 window cannot start: the Windows exe must
// never leave a headless process with no visible surface. ShellExecuteW
// returns a value above 32 on success; anything else is a failure.
//
// Like the POSIX launcher, the browser opens a 0600 redirect page, never
// the app URL itself: the bearer token is a URL fragment, and passing the
// URL here would put it in the browser's command line for any local user
// to read.
func Open(raw string) error {
	target, err := localURL(raw)
	if err != nil {
		return err
	}
	page, cleanup, err := bootstrapPage(target)
	if err != nil {
		return err
	}
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		cleanup()
		return err
	}
	url, err := windows.UTF16PtrFromString(page)
	if err != nil {
		cleanup()
		return err
	}
	shell32 := windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteW := shell32.NewProc("ShellExecuteW")
	ret, _, _ := procShellExecuteW.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(url)), 0, 0, 1 /* SW_SHOWNORMAL */)
	if ret <= 32 {
		cleanup()
		return errors.New("desktop: the default browser could not be opened")
	}
	// The browser reads the redirect after ShellExecuteW returns. Keep the
	// 0600 file briefly, then remove the whole private directory.
	time.AfterFunc(bootstrapPageTTL, cleanup)
	return nil
}
