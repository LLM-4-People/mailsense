//go:build windows

package bootstrap

import "golang.org/x/sys/windows"

// notify shows a native error box (user32 MessageBoxW).
func notify(text string) {
	t, err1 := windows.UTF16PtrFromString(text)
	c, err2 := windows.UTF16PtrFromString("Mailsense")
	if err1 != nil || err2 != nil {
		return
	}
	_, _ = windows.MessageBox(0, t, c, windows.MB_OK|windows.MB_ICONERROR|windows.MB_SETFOREGROUND)
}
