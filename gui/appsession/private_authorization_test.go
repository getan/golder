package appsession

import (
	"context"
	"strings"
	"testing"
)

func TestPrivateAuthorizationBlocksAllOwnedSessions(t *testing.T) {
	first := New(Options{})
	second := New(Options{})
	hub := NewSessionHub(first)
	hub.mu.Lock()
	hub.entries["second"] = &hubEntry{controller: second}
	hub.mu.Unlock()
	t.Cleanup(func() { _ = hub.Close() })
	second.running = true
	if _, err := hub.BeginPrivateAuthorization(); err == nil {
		t.Fatal("authorization began while another session was running")
	}
	if first.privateAuthorization || second.privateAuthorization {
		t.Fatal("failed acquisition left a controller blocked")
	}
	second.running = false
	release, err := hub.BeginPrivateAuthorization()
	if err != nil {
		t.Fatal(err)
	}
	if !first.privateAuthorization || !second.privateAuthorization {
		t.Fatal("authorization did not protect every existing controller")
	}
	if _, err := hub.NewSession(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "authorization") {
		t.Fatalf("new session was admitted during authorization: %v", err)
	}
	if _, err := hub.BeginPrivateAuthorization(); err == nil {
		t.Fatal("a second private authorization was admitted")
	}
	release()
	release() // cleanup is idempotent
	if first.privateAuthorization || second.privateAuthorization {
		t.Fatal("authorization guard was not released")
	}
}
