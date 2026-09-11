//go:build !windows

package runner

import "os/exec"

// prepareProcess is a no-op away from Windows. The deployment target is
// Windows Server; these stubs exist so the package still builds and tests on a
// developer's machine.
func prepareProcess(cmd *exec.Cmd) {}

// limitProcess does not cap memory on other platforms.
//
// The cap is deliberately not emulated with setrlimit. A partial guarantee
// that only holds on the platform nobody deploys to would invite trusting it
// on the one they do.
func limitProcess(pid int, memoryBytes int64) (func(), error) {
	return func() {}, nil
}
