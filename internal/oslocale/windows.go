//go:build windows

package oslocale

import (
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows kernel32 procedures. Find checks availability before Call:
// LazyProc.Call panics when the procedure is absent, and detection must
// never take the app down.
var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGetUserDefaultUILang = kernel32.NewProc("GetUserDefaultUILanguage")
	procLCIDToLocaleName     = kernel32.NewProc("LCIDToLocaleName")
	procGetUserDefaultLocale = kernel32.NewProc("GetUserDefaultLocaleName")

	procsOnce sync.Once
	procs     struct {
		uiLanguage  *windows.LazyProc
		lcidName    *windows.LazyProc
		defaultName *windows.LazyProc
	}
)

const (
	localeNameMax = 85 // LOCALE_NAME_MAX_LENGTH
	sortDefault   = 0
)

func loadProcs() {
	procsOnce.Do(func() {
		if err := procGetUserDefaultUILang.Find(); err == nil {
			procs.uiLanguage = procGetUserDefaultUILang
		}
		if err := procLCIDToLocaleName.Find(); err == nil {
			procs.lcidName = procLCIDToLocaleName
		}
		if err := procGetUserDefaultLocale.Find(); err == nil {
			procs.defaultName = procGetUserDefaultLocale
		}
	})
}

// detect prefers the UI/display language, which chooses the message
// catalog, then the user's regional locale, which is only a formatting
// hint. Either may be unavailable on an old or locked-down system.
func detect() Locale {
	loadProcs()
	if procs.uiLanguage != nil && procs.lcidName != nil {
		if raw := userUILanguage(procs.uiLanguage); raw != 0 {
			name := localeForLANGID(uint16(raw), lcidToLocaleName)
			if language, locale := Parse(name); language != "" {
				return Locale{Language: language, Locale: locale}
			}
		}
	}
	if procs.defaultName != nil {
		if name := userDefaultLocaleName(procs.defaultName); name != "" {
			if language, locale := Parse(name); language != "" {
				return Locale{Language: language, Locale: locale}
			}
		}
	}
	return Locale{}
}

// localeForLANGID builds MAKELCID(langid, SORT_DEFAULT) and asks Windows
// for its BCP-47 name. resolve is injected so the conversion is testable.
func localeForLANGID(langid uint16, resolve func(uint32) string) string {
	lcid := uint32(langid) | uint32(sortDefault)<<16
	return resolve(lcid)
}

// userUILanguage calls GetUserDefaultUILanguage, recovering from an
// unexpected failure instead of panicking the app.
func userUILanguage(proc *windows.LazyProc) (langid uintptr) {
	defer func() {
		if recover() != nil {
			langid = 0
		}
	}()
	r1, _, _ := proc.Call()
	return r1
}

// lcidToLocaleName calls LCIDToLocaleName. A return of zero or one means
// the LCID has no name (or the buffer was too small, which cannot happen
// with localeNameMax); the caller then uses the regional fallback.
func lcidToLocaleName(lcid uint32) (name string) {
	loadProcs()
	if procs.lcidName == nil {
		return ""
	}
	defer func() {
		if recover() != nil {
			name = ""
		}
	}()
	buf := make([]uint16, localeNameMax)
	n, _, _ := procs.lcidName.Call(uintptr(lcid), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0)
	if n <= 1 {
		return ""
	}
	return windows.UTF16ToString(buf[:n-1])
}

// userDefaultLocaleName calls GetUserDefaultLocaleName.
func userDefaultLocaleName(proc *windows.LazyProc) (name string) {
	defer func() {
		if recover() != nil {
			name = ""
		}
	}()
	buf := make([]uint16, localeNameMax)
	n, _, _ := proc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n <= 1 {
		return ""
	}
	return windows.UTF16ToString(buf[:n-1])
}
