//go:build windows

package outlook

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// outlookExe is classic Outlook's executable (its process name and App
// Paths key).
const outlookExe = "OUTLOOK.EXE"

// Started is the start time of the OUTLOOK.EXE process of this Windows
// session (the newest when several run): a Toolhelp process snapshot,
// then GetProcessTimes on a limited-information handle. No COM call:
// Outlook is not asked anything.
func (h *winHost) Started() (time.Time, error) {
	var mine uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &mine); err != nil {
		return time.Time{}, fmt.Errorf("ProcessIdToSessionId: %w", err)
	}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return time.Time{}, fmt.Errorf("CreateToolhelp32Snapshot: %w", err)
	}
	defer windows.CloseHandle(snap)
	var newest time.Time
	var failed error
	e := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		var session uint32
		if !strings.EqualFold(windows.UTF16ToString(e.ExeFile[:]), outlookExe) ||
			windows.ProcessIdToSessionId(e.ProcessID, &session) != nil || session != mine {
			continue
		}
		at, err := processStart(e.ProcessID)
		if err != nil {
			failed = err
			continue
		}
		if at.After(newest) {
			newest = at
		}
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return time.Time{}, fmt.Errorf("Process32Next: %w", err)
	}
	switch {
	case !newest.IsZero():
		return newest, nil
	case failed != nil:
		return time.Time{}, fmt.Errorf("outlook: cannot read Outlook's start time: %w", failed)
	}
	return time.Time{}, ErrNotRunning
}

func processStart(pid uint32) (time.Time, error) {
	p, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return time.Time{}, fmt.Errorf("OpenProcess: %w", err)
	}
	defer windows.CloseHandle(p)
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(p, &created, &exited, &kernel, &user); err != nil {
		return time.Time{}, fmt.Errorf("GetProcessTimes: %w", err)
	}
	return time.Unix(0, created.Nanoseconds()).UTC(), nil
}

var (
	procGetLastInputInfo = windows.NewLazySystemDLL("user32.dll").NewProc("GetLastInputInfo")
	procGetTickCount     = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetTickCount")
)

// lastInputInfo is LASTINPUTINFO (winuser.h).
type lastInputInfo struct {
	size uint32
	time uint32 // GetTickCount at the last input
}

// UserIdle is the time since the last keyboard or mouse input of this
// session (GetLastInputInfo). Both ticks are 32-bit GetTickCount values,
// so the unsigned difference stays right across the 49.7-day wrap; a
// difference above 2^31 ms (the value is documented as not guaranteed to
// be incremental) reads as input just now.
func (h *winHost) UserIdle() (time.Duration, error) {
	li := lastInputInfo{size: uint32(unsafe.Sizeof(lastInputInfo{}))}
	if ok, _, err := procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&li))); ok == 0 {
		return 0, fmt.Errorf("GetLastInputInfo: %w", err)
	}
	now, _, _ := procGetTickCount.Call()
	return idleSince(uint32(now), li.time), nil
}
