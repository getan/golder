package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/seatbelt"
	"github.com/getan/golder/internal/toolworker"
)

// The test binary has the GUI's early worker dispatch, so Tool re-executes it
// exactly as it re-executes the installed GUI executable.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == toolworker.Flag {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type workerTestRunner struct{}

func (workerTestRunner) SandboxCall(argv []string, _ string, _ seatbelt.CallPolicy) ([]string, func(), error) {
	return argv, nil, nil // exercises the subprocess protocol without requiring a nested OS sandbox
}

func TestGUIFileWorkerReadPatchAndRewind(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "hello.txt")
	if err := os.WriteFile(file, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	snap := agenttool.NewFileSnapshotRecorder()
	for _, tc := range []struct {
		name string
		tool agentcore.AgentTool
		args any
		want string
	}{
		{"read", &agenttool.ReadTool{Root: root}, map[string]string{"path": "hello.txt"}, "before"},
		{"apply_patch", &agenttool.ApplyPatchTool{Root: root, Snap: snap}, map[string]string{
			"patch": "*** Begin Patch\n*** Update File: hello.txt\n@@\n-before\n+after\n*** End Patch\n"}, "Applied 1 change"},
	} {
		raw, err := json.Marshal(tc.args)
		if err != nil {
			t.Fatal(err)
		}
		tool := &toolworker.Tool{Original: tc.tool, Root: root, Snap: snap, Runner: workerTestRunner{}, Mode: permissions.New(permissions.Auto)}
		result, err := tool.Execute(context.Background(), "test", raw, nil)
		if err != nil || result.IsError || !strings.Contains(agentcore.ContentToText(result.Content), tc.want) {
			t.Fatalf("%s: result=%q err=%v", tc.name, agentcore.ContentToText(result.Content), err)
		}
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != "after\n" {
		t.Fatalf("patched content = %q, err=%v", got, err)
	}
	if !snap.Commit("leaf", "test") {
		t.Fatal("worker did not transfer rewind baseline")
	}
	if _, _, warnings, err := snap.Restore(0); err != nil || len(warnings) != 0 {
		t.Fatalf("rewind: err=%v warnings=%v", err, warnings)
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != "before\n" {
		t.Fatalf("rewound content = %q, err=%v", got, err)
	}
}
