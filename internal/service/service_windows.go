//go:build windows

// Windows-service integration: `unitrise-gate service install` registers the
// agent with the SCM (auto-start, delayed), mirroring how the storEDGE Gate
// agent lives on site machines. `run` detects service context automatically.
package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const Name = "UnitRiseGateBridge"

// IsWindowsService reports whether we were launched by the SCM.
func IsWindowsService() bool {
	is, _ := svc.IsWindowsService()
	return is
}

type handler struct {
	run func(ctx context.Context)
}

func (h *handler) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.run(ctx); close(done) }()
	status <- svc.Status{State: svc.Running, Accepts: accepted}
	for {
		select {
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				<-done
				return false, 0
			}
		case <-done:
			return false, 0
		}
	}
}

// RunAsService blocks inside the SCM lifecycle.
func RunAsService(run func(ctx context.Context)) error {
	return svc.Run(Name, &handler{run: run})
}

func Install() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("open service manager (run as Administrator): %w", err)
	}
	defer m.Disconnect()
	if s, err := m.OpenService(Name); err == nil {
		s.Close()
		return fmt.Errorf("service %s already installed", Name)
	}
	s, err := m.CreateService(Name, exe, mgr.Config{
		DisplayName:      "UnitRise Gate Bridge",
		Description:      "Syncs gate access codes from UnitRise to the on-site gate software.",
		StartType:        mgr.StartAutomatic,
		DelayedAutoStart: true,
	}, "run")
	if err == nil {
		// Recovery actions: nobody watches a gate PC, so a crashed agent must
		// restart itself (5s, 30s, then every 60s; counter resets daily). The
		// suspensions-not-propagating alarm still fires from missed
		// heartbeats, but self-healing beats alarming.
		if rerr := s.SetRecoveryActions([]mgr.RecoveryAction{
			{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
			{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
			{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
		}, 86400); rerr != nil {
			fmt.Fprintf(os.Stderr, "warning: couldn't set service recovery actions: %v\n", rerr)
		}
	}
	if err != nil {
		return err
	}
	defer s.Close()
	return nil
}

func Uninstall() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(Name)
	if err != nil {
		return fmt.Errorf("service %s not installed", Name)
	}
	defer s.Close()
	return s.Delete()
}

func Start() error { return sc("start") }
func Stop() error  { return sc("stop") }

// Installed reports whether the service is registered (used by the built-in
// installer to decide between fresh-install and update-in-place).
func Installed() bool {
	m, err := mgr.Connect()
	if err != nil {
		return false
	}
	defer m.Disconnect()
	s, err := m.OpenService(Name)
	if err != nil {
		return false
	}
	s.Close()
	return true
}

func sc(verb string) error {
	out, err := exec.Command("sc", verb, Name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("sc %s: %v\n%s", verb, err, out)
	}
	return nil
}
