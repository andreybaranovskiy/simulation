//go:build !windows

package plugin

import (
	"os/exec"
	"syscall"
)

// The platform target is Windows Server; these are the portable fallbacks that
// keep the package building and testable elsewhere. They provide no memory cap,
// so an operator running the plugin feature off Windows should treat the
// isolation as weaker than the documented one.

func exeSuffix() string { return "" }

func newProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

func limitProcess(int, int64) (func(), error) {
	return func() {}, nil
}
