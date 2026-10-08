package platform

import (
	"fmt"
	"sync"
	"time"
)

// Service states, named as Windows names them; Linux states are mapped onto them.
const (
	Running      = "Running"
	Stopped      = "Stopped"
	StartPending = "StartPending"
	StopPending  = "StopPending"
	Missing      = "Missing"
)

// ServiceInfo is a service's state and start type (Automatic, Manual or Disabled).
type ServiceInfo struct {
	State     string
	StartType string
}

// ServiceSpec describes a service OmniBox registers.
type ServiceSpec struct {
	Name        string
	DisplayName string
	Description string
	Executable  string
	Args        []string
	Automatic   bool // start with the system; engines are manual
}

// Services controls operating system services: SCM on Windows, systemd on Linux.
type Services interface {
	Query(name string) (ServiceInfo, error)
	Start(name string) error
	// Stop waits until the service stopped or the timeout passed.
	Stop(name string, timeout time.Duration) error
	Install(spec ServiceSpec) error
	Remove(name string, timeout time.Duration) error
	// Change points an existing service at a new program and arguments (an update).
	Change(spec ServiceSpec) error
	// Executable is the program a service runs.
	Executable(name string) (string, error)
	// SetAutomatic makes a service start with the system, or only when started.
	SetAutomatic(name string, on bool) error
}

// FakeServices is an in-memory service manager for tests.
type FakeServices struct {
	mu       sync.Mutex
	services map[string]*fakeService
	// Calls records start:<name> and stop:<name> in order.
	Calls []string
	// OnStart runs when a service starts, e.g. to bring up a fake engine API.
	OnStart func(name string) error
}

type fakeService struct {
	info ServiceInfo
	spec ServiceSpec
}

// NewFakeServices returns an empty fake service manager.
func NewFakeServices() *FakeServices { return &FakeServices{services: map[string]*fakeService{}} }

// Set defines a service in a given state.
func (f *FakeServices) Set(name, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.services[name] = &fakeService{info: ServiceInfo{State: state, StartType: "Manual"}}
}

func (f *FakeServices) Query(name string) (ServiceInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.services[name]
	if !ok {
		return ServiceInfo{State: Missing}, nil
	}
	return s.info, nil
}

func (f *FakeServices) Start(name string) error {
	f.mu.Lock()
	s, ok := f.services[name]
	f.Calls = append(f.Calls, "start:"+name)
	hook := f.OnStart
	f.mu.Unlock()
	if !ok {
		return fmt.Errorf("служба %s не установлена", name)
	}
	if hook != nil {
		if err := hook(name); err != nil {
			return err
		}
	}
	f.mu.Lock()
	s.info.State = Running
	f.mu.Unlock()
	return nil
}

func (f *FakeServices) Stop(name string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, "stop:"+name)
	if s, ok := f.services[name]; ok {
		s.info.State = Stopped
	}
	return nil
}

func (f *FakeServices) Install(spec ServiceSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	start := "Manual"
	if spec.Automatic {
		start = "Automatic"
	}
	f.services[spec.Name] = &fakeService{info: ServiceInfo{State: Stopped, StartType: start}, spec: spec}
	f.Calls = append(f.Calls, "install:"+spec.Name)
	return nil
}

func (f *FakeServices) Remove(name string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.services, name)
	f.Calls = append(f.Calls, "remove:"+name)
	return nil
}

// Spec returns what a fake service was installed with.
func (f *FakeServices) Spec(name string) (ServiceSpec, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.services[name]
	if !ok {
		return ServiceSpec{}, false
	}
	return s.spec, true
}

func (f *FakeServices) Change(spec ServiceSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.services[spec.Name]
	if !ok {
		return fmt.Errorf("служба %s не установлена", spec.Name)
	}
	f.Calls = append(f.Calls, "change:"+spec.Name)
	start := s.info.StartType
	s.spec = spec
	s.info.StartType = start
	return nil
}

func (f *FakeServices) SetAutomatic(name string, on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.services[name]
	if !ok {
		return fmt.Errorf("служба %s не установлена", name)
	}
	s.spec.Automatic, s.info.StartType = on, "Manual"
	if on {
		s.info.StartType = "Automatic"
	}
	return nil
}

func (f *FakeServices) Executable(name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.services[name]
	if !ok {
		return "", fmt.Errorf("служба %s не установлена", name)
	}
	return s.spec.Executable, nil
}
