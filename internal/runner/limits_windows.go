//go:build windows

package runner

import (
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// prepareProcess sets creation flags before a runner starts.
//
// A new process group means the runner does not receive the console's Ctrl+C,
// so stopping the server from a terminal shuts runs down through the
// dispatcher's own cancellation rather than killing them mid-write.
func prepareProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP
}

// limitProcess caps a started process's memory with a Windows job object and
// returns a function that releases it.
//
// A memory cap is the difference between one runaway model and an unusable
// server. A model whose arrival rate outruns its service rate will consume
// everything the machine has, and the first casualty is the server supervising
// it.
//
// The job is created with kill-on-close, so if this process dies the operating
// system terminates the run rather than leaving it orphaned and still growing.
//
// There is a short window between the process starting and being assigned,
// during which it is uncapped. Closing it would need the process created
// suspended and its main thread resumed afterwards, which os/exec does not
// expose a handle for. The window covers flag parsing and a file open, well
// before a model has allocated anything, so the trade is worth taking rather
// than reimplementing process creation.
func limitProcess(pid int, memoryBytes int64) (func(), error) {
	if memoryBytes <= 0 {
		return func() {}, nil
	}

	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create job object: %w", err)
	}

	release := func() { _ = windows.CloseHandle(job) }

	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY |
				windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
		ProcessMemoryLimit: uintptr(memoryBytes),
	}

	_, err = windows.SetInformationJobObject(job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		uint32(unsafe.Sizeof(limits)))
	if err != nil {
		release()
		return nil, fmt.Errorf("set job memory limit: %w", err)
	}

	handle, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		release()
		return nil, fmt.Errorf("open the run process: %w", err)
	}
	defer windows.CloseHandle(handle)

	if err := windows.AssignProcessToJobObject(job, handle); err != nil {
		release()
		return nil, fmt.Errorf("assign the run to its job object: %w", err)
	}

	return release, nil
}
