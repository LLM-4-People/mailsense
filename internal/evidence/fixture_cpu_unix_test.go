//go:build fixture && unix

package evidence

import "syscall"

// cpuMillis is this process's CPU time (user + system) in milliseconds,
// from getrusage(RUSAGE_SELF).
func cpuMillis() int64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return -1
	}
	return ru.Utime.Nano()/1e6 + ru.Stime.Nano()/1e6
}
