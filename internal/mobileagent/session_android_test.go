//go:build android

package mobileagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/androidagent"
)

// TestAndroidToolsetRoundTrip exercises the real Android catalog through the
// mobile boundary: a read, an apply_patch write, and a final answer. It runs on
// a device or emulator; the host build only compiles it.
func TestAndroidToolsetRoundTrip(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	var mu sync.Mutex
	var tools []string
	gate := func(_ context.Context, call agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		mu.Lock()
		tools = append(tools, call.Name)
		mu.Unlock()
		return &agentcore.BeforeToolCallDecision{}
	}
	toolset, err := androidagent.New(androidagent.Config{Root: root, BeforeToolCall: gate})
	if err != nil {
		t.Fatalf("androidagent.New: %v", err)
	}
	defer toolset.Close()
	if strings.TrimSpace(toolset.Prompt()) == "" {
		t.Fatal("Android prompt is empty")
	}

	readTurn := agentcore.AssistantMessage{
		RoleField:  agentcore.RoleAssistant,
		Content:    agentcore.ContentList{agentcore.NewToolCallContent("read-1", "read", json.RawMessage(`{"path":"main.go"}`))},
		StopReason: agentcore.StopReasonToolUse,
	}
	patch := "*** Begin Patch\n*** Add File: created.txt\n+created on device\n*** End Patch\n"
	patchArgs, _ := json.Marshal(map[string]string{"patch": patch})
	patchTurn := agentcore.AssistantMessage{
		RoleField:  agentcore.RoleAssistant,
		Content:    agentcore.ContentList{agentcore.NewToolCallContent("patch-1", "apply_patch", patchArgs)},
		StopReason: agentcore.StopReasonToolUse,
	}
	session, err := New(Config{
		Workspace:    root,
		Model:        "fake",
		Provider:     "fake",
		SystemPrompt: toolset.Prompt(),
		Stream:       scriptedStream(readTurn, patchTurn, textMessage("finished", agentcore.StopReasonEndTurn)),
		Batch:        toolset.Batch,
		Permissions:  toolset.Permissions,
	})
	if err != nil {
		t.Fatalf("mobileagent.New: %v", err)
	}
	defer session.Close()

	if err := session.Start("read main.go, then add created.txt"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	events := collectRun(t, session)
	if got := joinedText(events); got != "finished" {
		t.Fatalf("final text = %q, events: %+v", got, events)
	}
	var readEnd, patchEnd *Event
	for i := range events {
		switch events[i].Type {
		case EventToolEnd:
			switch events[i].ToolName {
			case "read":
				readEnd = &events[i]
			case "apply_patch":
				patchEnd = &events[i]
			}
		}
	}
	if readEnd == nil || readEnd.IsError || !strings.Contains(readEnd.Text, "package main") {
		t.Fatalf("read result = %+v", readEnd)
	}
	if patchEnd == nil || patchEnd.IsError {
		t.Fatalf("apply_patch result = %+v", patchEnd)
	}
	raw, err := os.ReadFile(filepath.Join(root, "created.txt"))
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(raw) != "created on device\n" {
		t.Fatalf("written content = %q", raw)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(tools, ",") != "read,apply_patch" {
		t.Fatalf("host gate calls = %v, want read,apply_patch", tools)
	}
}
