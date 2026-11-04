//go:build linux

package resource

import (
	"bufio"
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// hostSampler reads the host through procfs and statfs. The previous CPU
// counters are kept because /proc/stat reports totals since boot: one
// percentage needs two readings.
type hostSampler struct {
	diskPath string

	mu        sync.Mutex
	prevIdle  uint64
	prevTotal uint64
	have      bool
}

// NewHostSampler returns the Linux host sampler for the volume that holds
// diskPath.
func NewHostSampler(diskPath string) Sampler {
	if diskPath == "" {
		diskPath = "/"
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
	idle, total, err := readCPUTimes()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.have && total > s.prevTotal {
		delta := total - s.prevTotal
		busy := delta - (idle - s.prevIdle)
		usage.CPU = Reading{Known: true, Percent: float64(busy) / float64(delta) * 100}
	}
	s.prevIdle, s.prevTotal, s.have = idle, total, true
	return nil
}

// readCPUTimes returns the idle and total jiffies of the aggregate cpu line.
// Guest time is already included in user time, so only the first eight
// fields are summed; adding guest would count it twice.
func readCPUTimes() (idle, total uint64, err error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			return 0, 0, errors.New("resource: /proc/stat cpu line is too short")
		}
		for i, field := range fields[1:9] {
			v, err := strconv.ParseUint(field, 10, 64)
			if err != nil {
				return 0, 0, err
			}
			total += v
			if i == 3 || i == 4 { // idle + iowait
				idle += v
			}
		}
		return idle, total, nil
	}
	if err := sc.Err(); err != nil {
		return 0, 0, err
	}
	return 0, 0, errors.New("resource: /proc/stat has no cpu line")
}

func memory(usage *Usage) error {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return err
	}
	defer f.Close()
	var total, available uint64
	var free, buffers, cached uint64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		v, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			total = v
		case "MemAvailable:":
			available = v
		case "MemFree:":
			free = v
		case "Buffers:":
			buffers = v
		case "Cached:":
			cached = v
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if total == 0 {
		return errors.New("resource: /proc/meminfo has no MemTotal")
	}
	if available == 0 {
		// Kernels before 3.14 have no MemAvailable: approximate it from
		// the other counters instead of reporting the whole host as used.
		available = free + buffers + cached
	}
	used := total - min(available, total)
	usage.RAM = Reading{Known: true, Percent: float64(used) / float64(total) * 100}
	return nil
}

func (s *hostSampler) disk(usage *Usage) error {
	var st syscall.Statfs_t
	if err := syscall.Statfs(s.diskPath, &st); err != nil {
		return err
	}
	if st.Blocks == 0 {
		return errors.New("resource: the data volume reports no blocks")
	}
	used := st.Blocks - st.Bfree
	usage.Disk = Reading{Known: true, Percent: float64(used) / float64(st.Blocks) * 100}
	return nil
}
