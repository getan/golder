package mobilebridge

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

type fakeBackend struct {
	mu       sync.Mutex
	started  []string
	polls    int
	snapshot string
	cancels  int
	closed   int
	startErr error
}

func (f *fakeBackend) Start(prompt string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return f.startErr
	}
	f.started = append(f.started, prompt)
	return nil
}
func (f *fakeBackend) PollJSON(max int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.polls++
	return `[{"seq":1,"type":"text","text":"hello"}]`, nil
}
func (f *fakeBackend) SnapshotJSON() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapshot, nil
}
func (f *fakeBackend) Cancel() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels++
}
func (f *fakeBackend) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
	return nil
}

func fakeFactory(backend *fakeBackend) Factory {
	return func(Config) (Backend, error) { return backend, nil }
}

func TestServiceHandlesOneSession(t *testing.T) {
	backend := &fakeBackend{snapshot: `{"running":false,"messages":[]}`}
	service := NewService(fakeFactory(backend))
	handle, err := service.Open(`{"workspace":"/tmp","model":"fake","apiKey":"secret-value"}`)
	if err != nil || handle == 0 {
		t.Fatalf("Open = %d, %v", handle, err)
	}
	if err := service.Start(handle, "hello"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	raw, err := service.PollJSON(handle, 10)
	if err != nil || raw == "" {
		t.Fatalf("PollJSON = %q, %v", raw, err)
	}
	if _, err := service.SnapshotJSON(handle); err != nil {
		t.Fatalf("SnapshotJSON: %v", err)
	}
	if err := service.Cancel(handle); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if err := service.Close(handle); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := service.Close(handle); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.started) != 1 || backend.started[0] != "hello" || backend.cancels != 1 || backend.closed != 1 {
		t.Fatalf("backend calls = %+v", backend)
	}
}

func TestServiceRejectsBadConfigAndKeepsSecretOutOfError(t *testing.T) {
	service := NewService(func(Config) (Backend, error) {
		return nil, errors.New("provider setup failed")
	})
	for _, raw := range []string{``, `{`, `{"workspace":"/tmp"}`, `{"model":"fake"}`} {
		if handle, err := service.Open(raw); err == nil || handle != 0 {
			t.Fatalf("Open(%q) = %d, %v; want failure", raw, handle, err)
		}
	}
	raw, _ := json.Marshal(map[string]string{"workspace": "/tmp", "model": "fake", "apiKey": "sk-do-not-echo"})
	if _, err := service.Open(string(raw)); err == nil {
		t.Fatal("factory failure was hidden")
	}
	if strings.Contains(service.LastError(), "sk-do-not-echo") {
		t.Fatalf("LastError leaked the API key: %q", service.LastError())
	}
}

func TestServiceUnknownHandleAndClosedSession(t *testing.T) {
	service := NewService(fakeFactory(&fakeBackend{}))
	if err := service.Start(99, "x"); err == nil {
		t.Fatal("unknown handle was accepted")
	}
	handle, err := service.Open(`{"workspace":"/tmp","model":"fake"}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Close(handle); err != nil {
		t.Fatal(err)
	}
	if err := service.Start(handle, "x"); err == nil {
		t.Fatal("closed handle was accepted")
	}
}

func TestServiceStartErrorIsReported(t *testing.T) {
	backend := &fakeBackend{startErr: errors.New("busy")}
	service := NewService(fakeFactory(backend))
	handle, err := service.Open(`{"workspace":"/tmp","model":"fake"}`)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(handle)
	if err := service.Start(handle, "x"); err == nil || service.LastError() != "busy" {
		t.Fatalf("Start = %v, LastError = %q", err, service.LastError())
	}
}
