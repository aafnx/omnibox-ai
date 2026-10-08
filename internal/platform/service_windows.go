//go:build windows

package platform

import (
	"errors"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// SystemServices is the Windows Service Control Manager.
type SystemServices struct{}

// NewServices returns the system service manager.
func NewServices() Services { return SystemServices{} }

func open(name string) (*mgr.Mgr, *mgr.Service, error) {
	m, err := mgr.Connect()
	if err != nil {
		return nil, nil, err
	}
	s, err := m.OpenService(name)
	if err != nil {
		m.Disconnect()
		return nil, nil, err
	}
	return m, s, nil
}

func (SystemServices) Query(name string) (ServiceInfo, error) {
	m, s, err := open(name)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return ServiceInfo{State: Missing}, nil
	}
	if err != nil {
		return ServiceInfo{}, err
	}
	defer m.Disconnect()
	defer s.Close()
	status, err := s.Query()
	if err != nil {
		return ServiceInfo{}, err
	}
	cfg, err := s.Config()
	if err != nil {
		return ServiceInfo{}, err
	}
	return ServiceInfo{State: stateName(status.State), StartType: startName(cfg.StartType)}, nil
}

func stateName(s svc.State) string {
	switch s {
	case svc.Running:
		return Running
	case svc.Stopped:
		return Stopped
	case svc.StartPending, svc.ContinuePending:
		return StartPending
	case svc.StopPending, svc.PausePending, svc.Paused:
		return StopPending
	}
	return Stopped
}

func startName(t uint32) string {
	switch t {
	case mgr.StartAutomatic:
		return "Automatic"
	case mgr.StartDisabled:
		return "Disabled"
	}
	return "Manual"
}

func (SystemServices) Start(name string) error {
	m, s, err := open(name)
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()
	return s.Start()
}

func (SystemServices) Stop(name string, timeout time.Duration) error {
	m, s, err := open(name)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return nil
	}
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()
	status, err := s.Query()
	if err != nil {
		return err
	}
	if status.State != svc.Stopped {
		if _, err := s.Control(svc.Stop); err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
			return err
		}
	}
	deadline := time.Now().Add(timeout)
	for {
		status, err := s.Query()
		if err != nil {
			return err
		}
		if status.State == svc.Stopped {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("служба не остановилась за отведённое время")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (SystemServices) Install(spec ServiceSpec) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	start := uint32(mgr.StartManual)
	if spec.Automatic {
		start = mgr.StartAutomatic
	}
	s, err := m.CreateService(spec.Name, spec.Executable, mgr.Config{DisplayName: spec.DisplayName, Description: spec.Description, StartType: start}, spec.Args...)
	if err != nil {
		return err
	}
	defer s.Close()
	// A crashed panel restarts; engines restart only through the panel (owner's stop stays a stop).
	if spec.Automatic {
		_ = s.SetRecoveryActions([]mgr.RecoveryAction{{Type: mgr.ServiceRestart, Delay: 5 * time.Second}}, 86400)
	}
	return nil
}

func (SystemServices) Remove(name string, timeout time.Duration) error {
	if err := (SystemServices{}).Stop(name, timeout); err != nil {
		return err
	}
	m, s, err := open(name)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return nil
	}
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()
	return s.Delete()
}

// IsAdmin reports whether the process runs elevated.
func IsAdmin() bool { return windows.GetCurrentProcessToken().IsElevated() }

// commandLine is how the SCM stores a program with its arguments.
func commandLine(spec ServiceSpec) string {
	parts := []string{syscall.EscapeArg(spec.Executable)}
	for _, a := range spec.Args {
		parts = append(parts, syscall.EscapeArg(a))
	}
	return strings.Join(parts, " ")
}

func (SystemServices) Change(spec ServiceSpec) error {
	m, s, err := open(spec.Name)
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()
	c, err := s.Config()
	if err != nil {
		return err
	}
	c.BinaryPathName = commandLine(spec)
	if err := s.UpdateConfig(c); err != nil {
		return err
	}
	// Verify: a change the SCM silently ignored would leave the old program in place.
	after, err := s.Config()
	if err != nil {
		return err
	}
	if after.BinaryPathName != c.BinaryPathName {
		return errors.New("служба не приняла новую команду запуска")
	}
	return nil
}

func (SystemServices) SetAutomatic(name string, on bool) error {
	m, s, err := open(name)
	if err != nil {
		return err
	}
	defer m.Disconnect()
	defer s.Close()
	c, err := s.Config()
	if err != nil {
		return err
	}
	c.StartType = mgr.StartManual
	if on {
		c.StartType = mgr.StartAutomatic
	}
	return s.UpdateConfig(c)
}

func (SystemServices) Executable(name string) (string, error) {
	m, s, err := open(name)
	if err != nil {
		return "", err
	}
	defer m.Disconnect()
	defer s.Close()
	c, err := s.Config()
	if err != nil {
		return "", err
	}
	return firstArg(c.BinaryPathName), nil
}

// firstArg is the program of a Windows command line.
func firstArg(command string) string {
	command = strings.TrimSpace(command)
	if strings.HasPrefix(command, `"`) {
		if end := strings.Index(command[1:], `"`); end >= 0 {
			return command[1 : end+1]
		}
	}
	if i := strings.IndexByte(command, ' '); i >= 0 {
		return command[:i]
	}
	return command
}
