//go:build windows

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	serviceName = "TaskTimerServer"
	serviceDesc = "Task Timer gateway"
)

// runService runs the gateway under the Windows Service Control Manager when the
// process was launched by it. Started from a console, svc.IsWindowsService
// reports false and we return handled=false so main() runs in the foreground
// instead - which is what `service start`, and a developer double-clicking the
// exe, both want.
func runService() (bool, error) {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return false, err
	}
	if !isService {
		return false, nil
	}

	// Under the SCM there is no console for log output to reach, so redirect it to
	// a file beside the data the service already owns. Best-effort: if the log
	// cannot be opened the service still runs, it just logs nowhere.
	if f, err := openServiceLog(); err == nil {
		log.SetOutput(f)
	}

	return true, svc.Run(serviceName, &handler{})
}

type handler struct{}

// Execute is the SCM callback. It runs run() on a cancellable context and maps
// the Stop/Shutdown controls onto cancelling it, so the graceful-drain path the
// foreground SIGTERM uses also runs when Windows stops the service.
func (h *handler) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (ssec bool, errno uint32) {
	const accepts = svc.AcceptStop | svc.AcceptShutdown

	changes <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errc := make(chan error, 1)
	go func() { errc <- run(ctx) }()

	changes <- svc.Status{State: svc.Running, Accepts: accepts}

	for {
		select {
		case req := <-r:
			switch req.Cmd {
			case svc.Interrogate:
				changes <- req.CurrentStatus
			case svc.Stop, svc.Shutdown:
				changes <- svc.Status{State: svc.StopPending}
				cancel()
				<-errc // let run() finish its graceful shutdown before we report Stopped
				return false, 0
			default:
				// A control we did not advertise in Accepts; ignore it.
			}
		case err := <-errc:
			// run() returned on its own - almost always a failed bind or a config
			// error at startup. Report a non-zero exit so the SCM shows the service
			// as failed rather than as a clean stop nobody asked for.
			if err != nil {
				log.Printf("task-timer-server: %v", err)
				return false, 1
			}
			return false, 0
		}
	}
}

func openServiceLog() (*os.File, error) {
	dir := filepath.Join(programData(), "TaskTimerServer")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(filepath.Join(dir, "server.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}

func programData() string {
	if pd := os.Getenv("ProgramData"); pd != "" {
		return pd
	}
	return `C:\ProgramData`
}

// serviceControl implements
// `task-timer-server service <install|uninstall|start|stop>`, so the NSIS
// installer (and an administrator) can manage the service without a third-party
// tool. The binary registers itself: its own absolute path becomes the service's
// ImagePath, and on next start svc.IsWindowsService routes it into runService.
func serviceControl(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: task-timer-server service <install|uninstall|start|stop>")
	}
	switch args[0] {
	case "install":
		return installService()
	case "uninstall", "remove":
		return removeService()
	case "start":
		return startService()
	case "stop":
		return stopService()
	default:
		return fmt.Errorf("unknown service command %q (want install, uninstall, start or stop)", args[0])
	}
}

func installService() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolving this executable's path: %w", err)
	}
	if exe, err = filepath.Abs(exe); err != nil {
		return err
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connecting to the service manager (are you elevated?): %w", err)
	}
	defer m.Disconnect()

	if s, err := m.OpenService(serviceName); err == nil {
		s.Close()
		return fmt.Errorf("service %q already exists; uninstall it first", serviceName)
	}

	s, err := m.CreateService(serviceName, exe, mgr.Config{
		DisplayName: serviceDesc,
		Description: "Task Timer gateway: desktop clients sync their timed work sessions through it.",
		StartType:   mgr.StartAutomatic,
	})
	if err != nil {
		return fmt.Errorf("creating service %q: %w", serviceName, err)
	}
	defer s.Close()

	log.Printf("installed service %q -> %s", serviceName, exe)
	return nil
}

func removeService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connecting to the service manager (are you elevated?): %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("service %q is not installed", serviceName)
	}
	defer s.Close()

	// Best-effort stop first, so Delete does not leave a running service marked
	// for deletion until the next reboot.
	_, _ = s.Control(svc.Stop)

	if err := s.Delete(); err != nil {
		return fmt.Errorf("deleting service %q: %w", serviceName, err)
	}
	log.Printf("removed service %q", serviceName)
	return nil
}

func startService() error {
	s, m, err := openService()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()

	if err := s.Start(); err != nil {
		return fmt.Errorf("starting service %q: %w", serviceName, err)
	}
	log.Printf("started service %q", serviceName)
	return nil
}

func stopService() error {
	s, m, err := openService()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()

	if _, err := s.Control(svc.Stop); err != nil {
		return fmt.Errorf("stopping service %q: %w", serviceName, err)
	}
	log.Printf("stop signalled to service %q", serviceName)
	return nil
}

func openService() (*mgr.Service, *mgr.Mgr, error) {
	m, err := mgr.Connect()
	if err != nil {
		return nil, nil, fmt.Errorf("connecting to the service manager (are you elevated?): %w", err)
	}
	s, err := m.OpenService(serviceName)
	if err != nil {
		m.Disconnect()
		return nil, nil, fmt.Errorf("service %q is not installed", serviceName)
	}
	return s, m, nil
}
