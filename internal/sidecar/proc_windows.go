//go:build windows

package sidecar

import (
	"fmt"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/LLM-4-People/mailsense/internal/clock"
)

// jobEnder owns the job object the child runs in. The job has
// KILL_ON_JOB_CLOSE: closing its only handle ends the child, and so does
// the app's own exit (the OS closes the handle), however the app ends.
type jobEnder struct{ job windows.Handle }

// start creates the job, starts the child suspended and without a console
// window, puts it in the job, and only then lets it run, so it never runs
// outside the job.
func start(cmd *exec.Cmd) (ender, error) {
	job, err := newKillOnCloseJob()
	if err != nil {
		return nil, err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_SUSPENDED, HideWindow: true}
	if err := cmd.Start(); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	if err := assignAndResume(job, uint32(cmd.Process.Pid)); err != nil {
		_ = windows.CloseHandle(job) // the suspended child is in no job: end it directly
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	return jobEnder{job: job}, nil
}

func newKillOnCloseJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("create job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return 0, fmt.Errorf("set KILL_ON_JOB_CLOSE: %w", err)
	}
	return job, nil
}

func assignAndResume(job windows.Handle, pid uint32) error {
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return fmt.Errorf("open process %d: %w", pid, err)
	}
	err = windows.AssignProcessToJobObject(job, h)
	_ = windows.CloseHandle(h)
	if err != nil {
		return fmt.Errorf("assign process %d to its job: %w", pid, err)
	}
	return resumeThreads(pid)
}

// resumeThreads resumes the suspended main thread (os/exec does not keep
// the thread handle, so it is found through a thread snapshot).
func resumeThreads(pid uint32) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fmt.Errorf("thread snapshot: %w", err)
	}
	defer windows.CloseHandle(snap)
	te := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	resumed := 0
	for err = windows.Thread32First(snap, &te); err == nil; err = windows.Thread32Next(snap, &te) {
		if te.OwnerProcessID != pid {
			continue
		}
		th, oerr := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, te.ThreadID)
		if oerr != nil {
			return fmt.Errorf("open thread %d: %w", te.ThreadID, oerr)
		}
		_, rerr := windows.ResumeThread(th)
		_ = windows.CloseHandle(th)
		if rerr != nil {
			return fmt.Errorf("resume thread %d: %w", te.ThreadID, rerr)
		}
		resumed++
	}
	if resumed == 0 {
		return fmt.Errorf("no thread of process %d to resume", pid)
	}
	return nil
}

// end closes the job: the OS ends every process in it at once (llama-server
// keeps no state worth a graceful shutdown).
func (j jobEnder) end(*child, time.Duration, clock.Clock) { _ = windows.CloseHandle(j.job) }
