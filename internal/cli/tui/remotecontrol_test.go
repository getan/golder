package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/permissions"
)

// newRemoteTestSession builds a fresh run session over a temp-dir store for the
// remote-control lifecycle tests (no resume, no tools).
func newRemoteTestSession(t *testing.T) *runSession {
	t.Helper()
	store := newTestStore(t)
	s, _, err := newRunSessionWithStore(store, Options{})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}
	return s
}

// TestStartStopRemote covers the start→already-running→stop lifecycle on
// runSession: startRemote binds a listener and stores the session, a second
// start reports "already running" without replacing it, and stopRemote clears
// the session so a later start can rebind.
func TestStartStopRemote(t *testing.T) {
	s := newRemoteTestSession(t)
	if s.remote != nil {
		t.Fatal("remote should be nil before start")
	}

	url, err := s.startRemote()
	if err != nil {
		t.Fatalf("startRemote: %v", err)
	}
	if url == "" {
		t.Fatal("startRemote returned empty url")
	}
	if s.remote == nil {
		t.Fatal("remote should be set after start")
	}
	first := s.remote

	// A second start is a no-op that reports the existing url and an error,
	// leaving the running session untouched.
	url2, err := s.startRemote()
	if err == nil {
		t.Error("second startRemote should report already-running error")
	}
	if url2 != url {
		t.Errorf("second startRemote url = %q, want %q", url2, url)
	}
	if s.remote != first {
		t.Error("second startRemote must not replace the running session")
	}

	s.stopRemote()
	if s.remote != nil {
		t.Error("remote should be nil after stop")
	}

	// Stop again is a no-op.
	s.stopRemote()

	// After stopping, a fresh start rebinds cleanly.
	if _, err := s.startRemote(); err != nil {
		t.Fatalf("restart after stop: %v", err)
	}
	s.stopRemote()
}

// TestBuildConfigInstallsRemoteSeam asserts buildConfig only installs the
// BeforeToolCall confirm seam while a remote session is present, layered over
// the risk-judge baseline: the judge gate (sandbox floor) is always installed
// so static hard-denies hold even under up-front trust, and the remote seam is
// chained on top once /remote-control is running.
func TestBuildConfigInstallsRemoteSeam(t *testing.T) {
	s := newRemoteTestSession(t)

	cfg := s.buildConfig()
	seam := cfg.Batch.ToolExecutorConfig.BeforeToolCall
	if seam == nil {
		t.Fatal("BeforeToolCall should hold the judge gate when remote control is off")
	}
	// The baseline gate contains benign calls — this test session has no
	// reviewer, so the failed review routes bash into the sandbox (fail-closed
	// containment) instead of running it bare — and blocks static hard-denies.
	if dec := seam(t.Context(), agentcore.AgentToolCall{Name: "bash", Arguments: json.RawMessage(`{"command":"echo hi"}`)}); dec == nil || dec.Block || !dec.Sandbox {
		t.Errorf("judge baseline should contain benign bash with a sandbox request, got %+v", dec)
	}
	if dec := seam(t.Context(), agentcore.AgentToolCall{Name: "bash", Arguments: json.RawMessage(`{"command":"sudo rm -rf /"}`)}); dec == nil || !dec.Block {
		t.Error("judge baseline should block static hard-denies")
	}

	if _, err := s.startRemote(); err != nil {
		t.Fatalf("startRemote: %v", err)
	}
	defer s.stopRemote()

	if cfg := s.buildConfig(); cfg.Batch.ToolExecutorConfig.BeforeToolCall == nil {
		t.Error("BeforeToolCall should be installed when remote control is on")
	}
}

// TestBuildConfigFullAccessKeepsStaticFloor asserts the permission mode is
// read per call: full-access waives review and sandboxing, yet the static
// hard-deny floor still blocks.
func TestBuildConfigFullAccessKeepsStaticFloor(t *testing.T) {
	s := newRemoteTestSession(t)
	s.perms.Set(permissions.FullAccess)
	seam := s.buildConfig().Batch.ToolExecutorConfig.BeforeToolCall
	if seam == nil {
		t.Fatal("the permission gate should always be installed (static floor)")
	}
	if dec := seam(t.Context(), agentcore.AgentToolCall{Name: "bash", Arguments: json.RawMessage(`{"command":"echo hi"}`)}); dec != nil {
		t.Errorf("full-access should allow benign bash, got %+v", dec)
	}
	if dec := seam(t.Context(), agentcore.AgentToolCall{Name: "bash", Arguments: json.RawMessage(`{"command":"sudo rm -rf /"}`)}); dec == nil || !dec.Block {
		t.Error("full-access must keep the static hard-deny floor")
	}
}

// TestRemoteConfirmSeamAllowsWhenNoClient verifies the confirm seam is a no-op
// (returns nil = allow under up-front trust) when no browser is connected, so a
// running-but-unpaired server never blocks tool calls.
func TestRemoteConfirmSeamAllowsWhenNoClient(t *testing.T) {
	s := newRemoteTestSession(t)
	if _, err := s.startRemote(); err != nil {
		t.Fatalf("startRemote: %v", err)
	}
	defer s.stopRemote()

	// No client is paired, so hasClient() is false and the seam must allow.
	seam := remoteConfirmSeam(s.remote, nil, "/tmp/project")
	if d := seam(t.Context(), agentcore.AgentToolCall{Name: "bash"}); d != nil {
		t.Errorf("seam should allow (nil) with no client, got %+v", d)
	}
}

// TestRemoteInputIgnoredWhileRunning routes a remoteInputMsg into an idle vs a
// running Model: while a run is in flight the prompt is not started (a busy note
// is shown instead), and the listener is always re-issued so later submissions
// keep arriving.
func TestRemoteInputIgnoredWhileRunning(t *testing.T) {
	s := newRemoteTestSession(t)
	if _, err := s.startRemote(); err != nil {
		t.Fatalf("startRemote: %v", err)
	}
	defer s.stopRemote()

	m := NewModel(Options{})
	m.session = s
	m.running = true

	updated, _ := m.Update(remoteInputMsg{text: "do something"})
	got := updated.(Model)
	if !hasSystemBlockContaining(got.transcript, "a run is in progress") {
		t.Errorf("expected a busy note in the transcript while running, blocks=%v",
			blockTexts(got.transcript))
	}
	// A run in progress must not consume the remote prompt as a new turn.
	if !got.running {
		t.Error("model should still be running; the remote prompt must not start a turn")
	}
}

// hasSystemBlockContaining reports whether any transcript block's text contains
// sub (case-insensitive substring over the rendered block texts).
func hasSystemBlockContaining(t transcript, sub string) bool {
	for _, s := range blockTexts(t) {
		if strings.Contains(strings.ToLower(s), strings.ToLower(sub)) {
			return true
		}
	}
	return false
}
