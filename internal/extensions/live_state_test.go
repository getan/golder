//go:build unix

package extensions

import (
	"path/filepath"
	"testing"
	"time"
)

// A failed call is history. A call-scoped source (Lark) starts a fresh helper
// per operation, so one bad call must not poison its readiness; a persistent
// source really does lose its connection and reports failed until a reconnect.
func TestCallFailureSeparatesHistoryFromReadiness(t *testing.T) {
	at := time.Date(2026, 11, 18, 14, 32, 0, 0, time.Local)
	scoped := &source{state: "on demand"}
	scoped.noteCallFailure("lark: boom", true, at)
	if scoped.state != "on demand" {
		t.Errorf("call-scoped state = %q, want on demand", scoped.state)
	}
	if scoped.lastError != "lark: boom" || scoped.lastErrorScope != "call" || !scoped.lastErrorAt.Equal(at) {
		t.Errorf("history = %q/%q/%v, want the failed call recorded", scoped.lastError, scoped.lastErrorScope, scoped.lastErrorAt)
	}
	scoped.clearFailure()
	if scoped.lastError != "" || scoped.lastErrorScope != "" || !scoped.lastErrorAt.IsZero() {
		t.Errorf("clearFailure left %q/%q/%v", scoped.lastError, scoped.lastErrorScope, scoped.lastErrorAt)
	}

	persistent := &source{state: "ready"}
	persistent.noteCallFailure("connection reset", false, at)
	if persistent.state != "failed" {
		t.Errorf("persistent state = %q, want failed", persistent.state)
	}
}

func TestLastAttemptTextNamesTimeScopeAndError(t *testing.T) {
	at := time.Date(2026, 11, 18, 14, 32, 0, 0, time.Local)
	got := LastAttemptText(Snapshot{LastError: "boom", LastErrorScope: "call", LastErrorAt: &at})
	if want := "2026-11-18 14:32 · call: boom"; got != want {
		t.Errorf("LastAttemptText = %q, want %q", got, want)
	}
	if got := LastAttemptText(Snapshot{}); got != "" {
		t.Errorf("empty snapshot text = %q, want empty", got)
	}
}

// RefreshLiveState reads the shared login-state lock without starting a helper,
// so a list view can show the moment's truth rather than the last attempt.
func TestRefreshLiveStateReportsTheSharedLock(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	m, err := New(Options{ConfigPath: filepath.Join(t.TempDir(), "config.toml"), IgnoreSources: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })

	home := filepath.Join(t.TempDir(), "lark")
	ref := SourceRef{Kind: "mcp", ID: "lark"}
	s := &source{ref: ref, cfg: SourceConfig{Transport: "stdio", StateDir: true, Command: "golder-lark", stateHome: home}, state: "on demand"}
	m.sources[ref.String()] = s
	m.publish(s)

	m.RefreshLiveState()
	if snap, ok := m.Source(ref); !ok || snap.Busy {
		t.Fatalf("idle source: busy=%v found=%v, want false/true", snap.Busy, ok)
	}

	unlock, err := lockState(filepath.Join(filepath.Dir(home), ".lock"))
	if err != nil {
		t.Fatal(err)
	}
	m.RefreshLiveState()
	if snap, _ := m.Source(ref); !snap.Busy {
		t.Fatal("held shared lock was not reported as in use")
	}
	unlock()
	m.RefreshLiveState()
	if snap, _ := m.Source(ref); snap.Busy {
		t.Fatal("released shared lock still reported as in use")
	}
}
