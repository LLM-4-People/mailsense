//go:build windows

package desktop

import (
	"errors"
	"sync"

	webview "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

const (
	imageIcon     = 1
	lrDefaultSize = 0x00000040
	lrShared      = 0x00008000
	wmSetIcon     = 0x0080
	iconSmall     = 0
	iconBig       = 1
)

var (
	user32              = windows.NewLazySystemDLL("user32.dll")
	procLoadImageW      = user32.NewProc("LoadImageW")
	procSendMessageW    = user32.NewProc("SendMessageW")
	procGetModuleHandle = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW")
)

// Window is one WebView2 window. Create it and run it on the same locked OS
// thread; Destroy may be called from any goroutine (the runtime posts the
// close message).
type Window struct {
	view webview.WebView
	once sync.Once
}

// New creates the 1280×840 Mailsense window and navigates it to url, which
// must be the bootstrap URL (loopback root + token fragment). It is the
// caller's job to keep the thread locked across New and Run.
func New(url string) (*Window, error) {
	target, err := localURL(url)
	if err != nil {
		return nil, err
	}
	view := webview.NewWithOptions(webview.WebViewOptions{
		AutoFocus: true,
		WindowOptions: webview.WindowOptions{
			Title:  WindowTitle,
			Width:  WindowWidth,
			Height: WindowHeight,
			Center: true,
		},
	})
	if view == nil {
		return nil, errors.New("desktop: cannot create the WebView2 window (is the WebView2 runtime installed?)")
	}
	view.SetTitle(WindowTitle)
	setWindowIcon(view)
	view.Navigate(target)
	return &Window{view: view}, nil
}

// setWindowIcon puts the icon embedded by rsrc on the title bar and taskbar.
// It is best-effort: a build without the resource still opens the window,
// with the system's default icon.
func setWindowIcon(view webview.WebView) {
	hwnd := uintptr(view.Window())
	if hwnd == 0 {
		return
	}
	instance, _, _ := procGetModuleHandle.Call(0)
	if instance == 0 {
		return
	}
	if icon, _, _ := procLoadImageW.Call(instance, 1, imageIcon, 0, 0, lrDefaultSize|lrShared); icon != 0 {
		_, _, _ = procSendMessageW.Call(hwnd, wmSetIcon, iconBig, icon)
	}
	if icon, _, _ := procLoadImageW.Call(instance, 1, imageIcon, 16, 16, lrShared); icon != 0 {
		_, _, _ = procSendMessageW.Call(hwnd, wmSetIcon, iconSmall, icon)
	}
}

// Run runs the window's message loop until the window closes. It blocks.
func (w *Window) Run() {
	if w == nil || w.view == nil {
		return
	}
	w.view.Run()
}

// Destroy closes the window. It is idempotent and safe from another
// goroutine, which is what the app's shutdown path needs.
func (w *Window) Destroy() {
	if w == nil {
		return
	}
	w.once.Do(func() {
		if w.view != nil {
			w.view.Destroy()
		}
	})
}
