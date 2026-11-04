//go:build windows

package resource

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32                 = windows.NewLazySystemDLL("kernel32.dll")
	procGetSystemTimes       = kernel32.NewProc("GetSystemTimes")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
)

// memoryStatusEx mirrors MEMORYSTATUSEX from sysinfoapi.h. The embedded
// length is the API's version field: it must be the struct size.
type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

// hostSampler reads the host through the Win32 system APIs. GetSystemTimes
// reports cumulative counters since boot, so the previous pair is kept.
type hostSampler struct {
	diskPath string

	mu         sync.Mutex
	prevIdle   uint64
	prevKernel uint64
	prevUser   uint64
	have       bool
}

// NewHostSampler returns the Windows host sampler for the volume that holds
// diskPath.
func NewHostSampler(diskPath string) Sampler {
	if diskPath == "" {
		diskPath, _ = os.Getwd()
	}
	return &hostSampler{diskPath: diskPath}
}

func (s *hostSampler) Sample(ctx context.Context) (Usage, error) {
	if err := ctx.Err(); err != nil {
		return Usage{}, err
	}
	usage := Usage{SampledAt: time.Now()}
	var problems []error
	if err := s.cpu(&usage); err != nil {
		problems = append(problems, err)
	}
	if err := memory(&usage); err != nil {
		problems = append(problems, err)
	}
	if err := s.disk(&usage); err != nil {
		problems = append(problems, err)
	}
	if !usage.CPU.Known && !usage.RAM.Known && !usage.Disk.Known {
		return Usage{}, errors.Join(problems...)
	}
	return usage, nil
}

func (s *hostSampler) cpu(usage *Usage) error {
	var idle, kernel, user windows.Filetime
	r1, _, callErr := procGetSystemTimes.Call(
		uintptr(unsafe.Pointer(&idle)),
		uintptr(unsafe.Pointer(&kernel)),
		uintptr(unsafe.Pointer(&user)))
	if r1 == 0 {
		return callErr
	}
	idleTicks := filetimeTicks(idle)
	kernelTicks := filetimeTicks(kernel)
	userTicks := filetimeTicks(user)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.have && kernelTicks >= s.prevKernel && userTicks >= s.prevUser && idleTicks >= s.prevIdle {
		// Kernel time includes idle time, so the busy total is kernel+user
		// and the idle share is subtracted once.
		delta := (kernelTicks - s.prevKernel) + (userTicks - s.prevUser)
		idleDelta := idleTicks - s.prevIdle
		if delta > 0 && idleDelta <= delta {
			usage.CPU = Reading{Known: true, Percent: float64(delta-idleDelta) / float64(delta) * 100}
		}
	}
	s.prevIdle, s.prevKernel, s.prevUser, s.have = idleTicks, kernelTicks, userTicks, true
	return nil
}

func filetimeTicks(t windows.Filetime) uint64 {
	return uint64(t.HighDateTime)<<32 | uint64(t.LowDateTime)
}

func memory(usage *Usage) error {
	mem := memoryStatusEx{Length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	r1, _, callErr := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&mem)))
	if r1 == 0 {
		return callErr
	}
	usage.RAM = Reading{Known: true, Percent: float64(mem.MemoryLoad)}
	return nil
}

func (s *hostSampler) disk(usage *Usage) error {
	path, err := windows.UTF16PtrFromString(s.diskPath)
	if err != nil {
		return err
	}
	var available, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(path, &available, &total, &totalFree); err != nil {
		return err
	}
	if total == 0 {
		return errors.New("resource: the data volume reports no bytes")
	}
	used := total - min(totalFree, total)
	usage.Disk = Reading{Known: true, Percent: float64(used) / float64(total) * 100}
	return nil
}
