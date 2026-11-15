// Package mobilebridge exposes the mobile session boundary as a small,
// handle-based JSON API for native hosts. It deliberately has no UI or JNI
// dependency: the JNI entry points live in cmd/golder-mobile, and the service
// can be tested on a desktop with a fake backend.
package mobilebridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Version identifies the bridge ABI. A host must treat a mismatch as
// unsupported rather than guessing at field semantics.
const Version = "0.1"

// maxConfigBytes bounds one config string before it is decoded. It is a guard
// against a malformed bridge call, not a session limit.
const maxConfigBytes = 64 << 10

// Config is the host-owned setup for one mobile conversation. Secrets are
// accepted but never echoed in errors or events.
type Config struct {
	Workspace     string `json:"workspace"`
	Model         string `json:"model"`
	Provider      string `json:"provider,omitempty"`
	BaseURL       string `json:"baseUrl,omitempty"`
	Protocol      string `json:"protocol,omitempty"`
	APIKey        string `json:"apiKey,omitempty"`
	SystemPrompt  string `json:"systemPrompt,omitempty"`
	ContextWindow int    `json:"contextWindow,omitempty"`
	// StateDir, when set, enables local session persistence under
	// StateDir/sessions. SessionID selects an existing session; empty generates
	// one. The host supplies an app-private directory.
	StateDir  string `json:"stateDir,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
	// DebugLog is an optional host-only diagnostic path. The Android backend
	// appends loop event types and terminal errors (never prompts or secrets)
	// to it. Empty disables the log.
	DebugLog string `json:"debugLog,omitempty"`
}

// Backend is one live conversation. The Android implementation wraps
// mobileagent.Session; tests use a fake.
type Backend interface {
	Start(prompt string) error
	PollJSON(max int) (string, error)
	SnapshotJSON() (string, error)
	Cancel()
	Close() error
}

// Factory builds a backend from host config. It must not expose the API key in
// an error.
type Factory func(Config) (Backend, error)

type entry struct {
	mu      sync.Mutex
	closed  bool
	backend Backend
}

// Service is the handle registry behind the JNI/C ABI. Operations on different
// handles proceed independently; operations on one handle are serialized so a
// close cannot race an in-flight poll.
type Service struct {
	factory Factory

	mu      sync.Mutex
	next    int
	entries map[int]*entry
	lastErr string
}

// NewService builds a registry around factory.
func NewService(factory Factory) *Service {
	return &Service{factory: factory, entries: make(map[int]*entry)}
}

// Open decodes configJSON, builds a backend, and returns a handle > 0. A
// failure returns 0 and records LastError.
func (s *Service) Open(configJSON string) (handle int, err error) {
	if s == nil || s.factory == nil {
		return 0, s.fail(errors.New("mobilebridge: service is not configured"))
	}
	if len(configJSON) > maxConfigBytes {
		return 0, s.fail(fmt.Errorf("mobilebridge: config is above %d bytes", maxConfigBytes))
	}
	var cfg Config
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return 0, s.fail(fmt.Errorf("mobilebridge: decode config: %w", err))
	}
	if strings.TrimSpace(cfg.Workspace) == "" {
		return 0, s.fail(errors.New("mobilebridge: workspace is required"))
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return 0, s.fail(errors.New("mobilebridge: model is required"))
	}
	backend, err := s.factory(cfg)
	if err != nil {
		return 0, s.fail(err)
	}
	s.mu.Lock()
	s.next++
	handle = s.next
	s.entries[handle] = &entry{backend: backend}
	s.lastErr = ""
	s.mu.Unlock()
	return handle, nil
}

// Start begins one run on handle.
func (s *Service) Start(handle int, prompt string) error {
	e, err := s.entry(handle)
	if err != nil {
		return s.fail(err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return s.fail(errors.New("mobilebridge: session is closed"))
	}
	if err := e.backend.Start(prompt); err != nil {
		return s.fail(err)
	}
	s.clearErr()
	return nil
}

// PollJSON drains up to max events from handle.
func (s *Service) PollJSON(handle, max int) (string, error) {
	e, err := s.entry(handle)
	if err != nil {
		return "", s.fail(err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return "", s.fail(errors.New("mobilebridge: session is closed"))
	}
	raw, err := e.backend.PollJSON(max)
	if err != nil {
		return "", s.fail(err)
	}
	s.clearErr()
	return raw, nil
}

// SnapshotJSON returns one consistent state snapshot.
func (s *Service) SnapshotJSON(handle int) (string, error) {
	e, err := s.entry(handle)
	if err != nil {
		return "", s.fail(err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return "", s.fail(errors.New("mobilebridge: session is closed"))
	}
	raw, err := e.backend.SnapshotJSON()
	if err != nil {
		return "", s.fail(err)
	}
	s.clearErr()
	return raw, nil
}

// Cancel asks the backend to stop its active run.
func (s *Service) Cancel(handle int) error {
	e, err := s.entry(handle)
	if err != nil {
		return s.fail(err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	e.backend.Cancel()
	s.clearErr()
	return nil
}

// Close drops handle and closes its backend. It is idempotent for an unknown
// handle so a host's lifecycle cleanup can run twice.
func (s *Service) Close(handle int) error {
	s.mu.Lock()
	e := s.entries[handle]
	delete(s.entries, handle)
	s.mu.Unlock()
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil
	}
	e.closed = true
	if err := e.backend.Close(); err != nil {
		return s.fail(err)
	}
	s.clearErr()
	return nil
}

// LastError returns the most recent failure, or "".
func (s *Service) LastError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

func (s *Service) entry(handle int) (*entry, error) {
	s.mu.Lock()
	e := s.entries[handle]
	s.mu.Unlock()
	if e == nil {
		return nil, fmt.Errorf("mobilebridge: unknown session handle %d", handle)
	}
	return e, nil
}

func (s *Service) fail(err error) error {
	if err == nil {
		return nil
	}
	s.mu.Lock()
	s.lastErr = err.Error()
	s.mu.Unlock()
	return err
}

func (s *Service) clearErr() {
	s.mu.Lock()
	s.lastErr = ""
	s.mu.Unlock()
}
