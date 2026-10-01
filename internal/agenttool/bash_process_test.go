package agenttool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestBashToolCancelKillsProcessGroup cancels a pipeline whose shell forks
// children (`sleep 30 | cat`). The direct child (bash) alone dying would
// leave `sleep` holding the stdout pipe, so a fast return proves the whole
// process group was terminated, not just the direct child.
func TestBashToolCancelKillsProcessGroup(t *testing.T) {
	tool := &BashTool{Dir: t.TempDir()}
	raw, err := json.Marshal(map[string]any{"command": "sleep 30 | cat"})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, execErr := tool.Execute(ctx, "call-1", raw, nil)
		done <- execErr
	}()

	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case execErr := <-done:
		if execErr == nil || !strings.Contains(execErr.Error(), "canceled") {
			t.Fatalf("Execute error = %v, want a canceled error", execErr)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("cancel took %s, want a prompt process-group kill", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not return; the process group was not killed")
	}
}

// TestBashToolTimeoutKillsProcessGroup mirrors the cancel test through the
// timeout path: the command must not outlive its deadline via a forked child.
func TestBashToolTimeoutKillsProcessGroup(t *testing.T) {
	tool := &BashTool{Dir: t.TempDir()}
	raw, err := json.Marshal(map[string]any{"command": "sleep 30 | cat", "timeout_ms": 200})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}

	start := time.Now()
	_, execErr := tool.Execute(context.Background(), "call-1", raw, nil)
	if execErr == nil || !strings.Contains(execErr.Error(), "timed out") {
		t.Fatalf("Execute error = %v, want a timeout error", execErr)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("timeout took %s, want a prompt process-group kill", elapsed)
	}
}
