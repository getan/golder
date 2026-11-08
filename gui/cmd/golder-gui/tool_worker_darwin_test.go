//go:build darwin

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/seatbelt"
	"github.com/getan/golder/internal/toolworker"
)

// Verify the GUI executable's worker entry under the real macOS policy as well
// as the protocol-only tests. This does not allocate a PTY or request escape.
func TestGUIFileWorkerWithMacSandbox(t *testing.T) {
	root := t.TempDir()
	runner := seatbelt.New(root)
	if runner == nil {
		t.Skip("sandbox-exec unavailable")
	}
	file := filepath.Join(root, "hello.txt")
	if err := os.WriteFile(file, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pixel.png"), imageData.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	_, rgErr := exec.LookPath("rg")
	snap := agenttool.NewFileSnapshotRecorder()
	for _, tc := range []struct {
		tool agentcore.AgentTool
		args any
		want string
	}{
		{&agenttool.ReadTool{Root: root}, map[string]string{"path": "hello.txt"}, "before"},
		{&agenttool.GrepTool{Root: root}, map[string]string{"pattern": "before"}, "hello.txt"},
		{&agenttool.FindTool{Root: root}, map[string]string{"glob": "hello.txt"}, "hello.txt"},
		{&agenttool.LsTool{Root: root}, map[string]string{}, "hello.txt"},
		{&agenttool.ViewImageTool{Root: root}, map[string]string{"path": "pixel.png"}, "Viewed image"},
		{&agenttool.ApplyPatchTool{Root: root, Snap: snap}, map[string]string{
			"patch": "*** Begin Patch\n*** Update File: hello.txt\n@@\n-before\n+after\n*** End Patch\n"}, "Applied 1 change"},
	} {
		raw, err := json.Marshal(tc.args)
		if err != nil {
			t.Fatal(err)
		}
		tool := &toolworker.Tool{Original: tc.tool, Root: root, Snap: snap, Runner: runner, Mode: permissions.New(permissions.Auto)}
		result, err := tool.Execute(context.Background(), "test", raw, nil)
		text := agentcore.ContentToText(result.Content)
		if strings.Contains(text, "sandbox_apply: Operation not permitted") {
			t.Skip("outer sandbox does not permit nested sandbox-exec")
		}
		if rgErr != nil && (tc.tool.Name() == "grep" || tc.tool.Name() == "find") {
			if err != nil || !result.IsError || !strings.Contains(text, "ripgrep") {
				t.Fatalf("%s: missing ripgrep was not reported: result=%q err=%v", tc.tool.Name(), text, err)
			}
			continue
		}
		if err != nil || result.IsError || !strings.Contains(text, tc.want) {
			t.Fatalf("%s: result=%q err=%v", tc.tool.Name(), text, err)
		}
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != "after\n" {
		t.Fatalf("patch content = %q, err=%v", got, err)
	}
	if !snap.Commit("leaf", "test") {
		t.Fatal("sandboxed GUI worker did not transfer its rewind baseline")
	}
}
