//go:build unix

package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/seatbelt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A local MCP helper, not a real Lark account. Holding active with O_EXCL catches
// overlapping credential writers across independently launched processes.
func TestLarkStateMCPFixture(t *testing.T) {
	if os.Getenv("LARK_STATE_FIXTURE") != "1" {
		return
	}
	home := os.Getenv("HOME")
	s := mcp.NewServer(&mcp.Implementation{Name: "golder-lark", Version: "fixture"}, nil)
	s.AddTool(&mcp.Tool{Name: "echo", InputSchema: json.RawMessage(`{"type":"object","properties":{"hold":{"type":"boolean"}}}`)}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		active := filepath.Join(home, "active")
		f, err := os.OpenFile(active, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("concurrent state writer: %w", err)
		}
		_ = f.Close()
		defer os.Remove(active)
		var in struct {
			Hold bool `json:"hold"`
		}
		_ = json.Unmarshal(req.Params.Arguments, &in)
		if in.Hold {
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				if _, err := os.Stat(filepath.Join(home, "release")); err == nil {
					break
				}
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-ticker.C:
				}
			}
		}
		old, _ := os.ReadFile(filepath.Join(home, "refresh-state"))
		if err := os.WriteFile(filepath.Join(home, "refresh-state"), append(old, 'x'), 0o600); err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "lark-state-result"}}}, nil
	})
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func larkStateManagers(t *testing.T) (*Manager, *Manager, SourceRef) {
	t.Helper()
	if !seatbelt.Available() {
		t.Skip("OS extension sandbox unavailable")
	}
	t.Setenv("GOLDER_HOME", t.TempDir())
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(t.TempDir(), "golder-lark")
	input, err := os.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := os.OpenFile(helper, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatalf("copy helper: %v %v", copyErr, closeErr)
	}
	ref := SourceRef{Kind: "mcp", ID: "lark"}
	cfg := SourceConfig{
		Enabled: true, Transport: "stdio", Command: helper, StateDir: true,
		Args: []string{"-test.run=^TestLarkStateMCPFixture$"}, StartupTimeout: "5s", CallTimeout: "15s",
		Setup: []string{helper, "-test.run=^TestSetupFixtureChild$"}, SetupOrigins: []string{"https://auth.example.com"},
		Env: map[string]string{"LARK_STATE_FIXTURE": "1", "EXTENSION_SETUP_FIXTURE": "1"},
	}
	if err := WriteSource(ConfigPath(), ref, &cfg); err != nil {
		t.Fatal(err)
	}
	managers := make([]*Manager, 0, 2)
	for range 2 {
		m, err := New(Options{ConfigPath: ConfigPath(), Cwd: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = m.Close() })
		if err := m.ApproveSession(ref); err != nil {
			t.Fatal(err)
		}
		managers = append(managers, m)
	}
	return managers[0], managers[1], ref
}

func waitLarkStateFile(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for fixture marker %s", filepath.Base(path))
		case <-ticker.C:
		}
	}
}

func TestLarkStateManagersCanRemainConnectedAndSerializeBusinessCalls(t *testing.T) {
	a, b, ref := larkStateManagers(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, m := range []*Manager{a, b} {
		if err := m.Connect(ctx, ref); err != nil {
			t.Fatal(err)
		}
		view, _ := m.Source(ref)
		if view.State != "on demand" || len(m.Tools()) != 1 {
			t.Fatalf("logical connection not ready on demand: %+v", view)
		}
	}
	home := sourceHome(ref)
	path := filepath.Join(filepath.Dir(home), ".lock")
	unlock, err := lockState(path)
	if err != nil {
		t.Fatalf("idle MCP connections kept shared login state locked: %v", err)
	}
	unlock()
	callCtx := agentcore.WithExternalToolAuthorization(ctx)
	done := make(chan agentcore.AgentToolResult, 1)
	go func() {
		r, _ := a.Tools()[0].Execute(callCtx, "first", json.RawMessage(`{"hold":true}`), nil)
		done <- r
	}()
	waitLarkStateFile(t, ctx, filepath.Join(home, "active"))
	// Destructive maintenance must still refuse to race the active writer.
	if err := b.Reset(ref); !errors.Is(err, errStateInUse) {
		t.Fatalf("reset did not preserve active login state: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "active")); err != nil {
		t.Fatal("failed reset removed active state")
	}
	// Reconnect b while a is active. Catalog probing queues instead of claiming
	// an idle lifetime lock; release a so b can complete within its budget.
	second := make(chan agentcore.AgentToolResult, 1)
	go func() {
		r, _ := b.Tools()[0].Execute(callCtx, "second", json.RawMessage(`{}`), nil)
		second <- r
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		view, _ := b.Source(ref)
		if view.State == "connecting" {
			break
		}
		select {
		case r := <-second:
			t.Fatalf("second manager failed instead of queuing: %s", agentcore.ContentToText(r.Content))
		case <-ctx.Done():
			t.Fatal("second manager never queued its connection probe")
		case <-ticker.C:
		}
	}
	if err := os.WriteFile(filepath.Join(home, "release"), []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := <-done; r.IsError || !strings.Contains(agentcore.ContentToText(r.Content), "lark-state-result") {
		t.Fatalf("first call failed: %s", agentcore.ContentToText(r.Content))
	}
	if r := <-second; r.IsError || !strings.Contains(agentcore.ContentToText(r.Content), "lark-state-result") {
		t.Fatalf("second connected manager could not use the same login: %s", agentcore.ContentToText(r.Content))
	}
	if data, err := os.ReadFile(filepath.Join(home, "refresh-state")); err != nil || string(data) != "xx" {
		t.Fatalf("shared refresh writes were lost or duplicated: %q %v", data, err)
	}
	// An idle logical connection in a must not stop setup/reset from b.
	if err := b.Setup(ctx, ref, nil); err != nil {
		t.Fatalf("idle connection blocked explicit authorization: %v", err)
	}
	if err := b.Reset(ref); err != nil {
		t.Fatalf("idle connection blocked reset: %v", err)
	}
}

func TestLarkStateLockCrossProcessFixture(t *testing.T) {
	if os.Getenv("LARK_STATE_LOCK_FIXTURE") != "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := os.WriteFile(os.Getenv("LARK_STATE_LOCK_PATH")+".ready", []byte("ready"), 0o600); err != nil {
		os.Exit(2)
	}
	unlock, err := lockStateContext(ctx, os.Getenv("LARK_STATE_LOCK_PATH"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	unlock()
	fmt.Print("acquired")
	os.Exit(0)
}

func TestLarkStateManagersCancelActiveRequestBeforeReleasingLease(t *testing.T) {
	a, b, ref := larkStateManagers(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	for _, m := range []*Manager{a, b} {
		if err := m.Connect(ctx, ref); err != nil {
			t.Fatal(err)
		}
	}
	activeCtx, cancelActive := context.WithCancel(agentcore.WithExternalToolAuthorization(ctx))
	done := make(chan agentcore.AgentToolResult, 1)
	go func() {
		r, _ := a.Tools()[0].Execute(activeCtx, "cancel-active", json.RawMessage(`{"hold":true}`), nil)
		done <- r
	}()
	home := sourceHome(ref)
	waitLarkStateFile(t, ctx, filepath.Join(home, "active"))
	cancelActive()
	select {
	case r := <-done:
		if !r.IsError {
			t.Fatal("cancelled active request was reported successful")
		}
	case <-ctx.Done():
		t.Fatal("active cancellation did not drain the helper")
	}
	if _, err := os.Stat(filepath.Join(home, "active")); !os.IsNotExist(err) {
		t.Fatalf("caller returned before the helper finished state cleanup: %v", err)
	}
	r, err := b.Tools()[0].Execute(agentcore.WithExternalToolAuthorization(ctx), "after-cancel", json.RawMessage(`{}`), nil)
	if err != nil || r.IsError {
		t.Fatalf("cancelled helper kept other conversations blocked: %v %s", err, agentcore.ContentToText(r.Content))
	}
	if data, err := os.ReadFile(filepath.Join(home, "refresh-state")); err != nil || string(data) != "x" {
		t.Fatalf("cancelled request was replayed or interfered with the next writer: %q %v", data, err)
	}
}

func TestLarkStateLeaseCoordinatesIndependentHostProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".lock")
	unlock, err := lockState(path)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	exe, _ := os.Executable()
	cmd := exec.Command(exe, "-test.run=^TestLarkStateLockCrossProcessFixture$")
	cmd.Env = append(os.Environ(), "LARK_STATE_LOCK_FIXTURE=1", "LARK_STATE_LOCK_PATH="+path)
	done := make(chan error, 1)
	go func() {
		output, err := cmd.CombinedOutput()
		if err == nil && string(output) != "acquired" {
			err = fmt.Errorf("unexpected child response %q", output)
		}
		done <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	waitLarkStateFile(t, ctx, path+".ready")
	select {
	case err := <-done:
		t.Fatalf("another host acquired the active state lease: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("another host could not acquire the released lease")
	}
}
