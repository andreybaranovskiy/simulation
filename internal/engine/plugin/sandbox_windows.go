//go:build windows

package plugin

import (
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func exeSuffix() string { return ".exe" }

// newProcessGroup detaches the plugin from the server's console, so stopping
// the server from a terminal does not deliver its Ctrl+C to a build or a
// generate step mid-write.
func newProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP
}

// limitProcess caps a plugin process's memory with a Windows job object and
// returns a function that releases it. This is the same mechanism the run
// dispatcher uses, and for the same reason: it is the difference between one
// runaway program and an unusable server.
//
// The job is kill-on-close, so if the server dies the operating system tears
// the plugin down rather than leaving it orphaned and still growing.
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
		return nil, fmt.Errorf("open the plugin process: %w", err)
	}
	defer windows.CloseHandle(handle)

	if err := windows.AssignProcessToJobObject(job, handle); err != nil {
		release()
		return nil, fmt.Errorf("assign the plugin to its job object: %w", err)
	}

	return release, nil
}
