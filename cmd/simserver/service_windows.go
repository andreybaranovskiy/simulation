//go:build windows

package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows/svc"
)

// serviceName is the name the server registers under with the service control
// manager. deploy/iis/install-service.ps1 uses the same name.
const serviceName = "SimulationPlatform"

// runAsService hands control to the service control manager when the process
// was started by it, and returns true once that path has run. When the process
// was started from a console instead, it returns false so main runs the server
// in the foreground.
func runAsService() bool {
	isService, err := svc.IsWindowsService()
	if err != nil {
		fmt.Fprintln(os.Stderr, "simserver: could not determine service context:", err)
		os.Exit(1)
	}
	if !isService {
		return false
	}

	if err := svc.Run(serviceName, &handler{}); err != nil {
		// There is no console here to print to; the failure is recorded by the
		// SCM and, if the config set one, the log file.
		os.Exit(1)
	}
	return true
}

// handler bridges the service control manager to the server. It reports the
// lifecycle states the SCM expects and turns a stop or a shutdown into the
// clean cancellation the server already knows how to unwind.
type handler struct{}

func (h *handler) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}

	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- run(stop) }()

	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}

	for {
		select {
		case err := <-done:
			// The server exited on its own, usually because it failed to start.
			// A non-zero exit code tells the SCM the service faulted.
			if err != nil {
				return false, 1
			}
			return false, 0

		case req := <-requests:
			switch req.Cmd {
			case svc.Interrogate:
				status <- req.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				close(stop)
				<-done
				return false, 0
			}
		}
	}
}
