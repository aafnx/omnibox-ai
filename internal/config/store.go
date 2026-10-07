package config

import (
	"sync"
	"sync/atomic"
)

// Store keeps the current configuration in memory and is its only writer. Readers get an
// immutable snapshot; a change is applied to a copy, validated, written to disk and only then
// becomes current, so a failed save never leaves half a change in memory.
type Store struct {
	path    string
	current atomic.Pointer[Config]
	write   sync.Mutex
}

// Open loads config.json into a store.
func Open(path string) (*Store, error) {
	c, err := Load(path)
	if err != nil {
		return nil, err
	}
	return NewStore(path, c), nil
}

// NewStore wraps an already loaded configuration.
func NewStore(path string, c *Config) *Store {
	s := &Store{path: path}
	s.current.Store(c)
	return s
}

// Get returns the current configuration. Callers must not modify it.
func (s *Store) Get() *Config { return s.current.Load() }

// Update applies change to a copy, saves it and makes it current.
func (s *Store) Update(change func(c *Config) error) error {
	s.write.Lock()
	defer s.write.Unlock()
	next := s.current.Load().Clone()
	if err := change(next); err != nil {
		return err
	}
	if err := next.Save(s.path); err != nil {
		return err
	}
	s.current.Store(next)
	return nil
}
