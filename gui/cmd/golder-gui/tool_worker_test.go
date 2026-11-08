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
	"github.com/getan/golder/internal/cli/run"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/seatbelt"
	"github.com/getan/golder/internal/toolworker"
)

// The test binary has the GUI's early worker dispatch, so Tool re-executes it
// exactly as it re-executes the installed GUI executable.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && (os.Args[1] == toolworker.Flag || os.Args[1] == seatbelt.TerminalBootstrapFlag) {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type workerTestRunner struct{}

func (workerTestRunner) SandboxCall(argv []string, _ string, _ seatbelt.CallPolicy) ([]string, func(), error) {
	return argv, nil, nil // exercises the subprocess protocol without requiring a nested OS sandbox
}

func TestGUIHelperFlagsDispatchBeforeNormalFlags(t *testing.T) {
	for _, tc := range []struct{ flag, want string }{
		{toolworker.Flag, "golder-gui: tool worker:"},
		{seatbelt.TerminalBootstrapFlag, "golder-gui: terminal sandbox:"},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], tc.flag)
			cmd.Stdin = strings.NewReader("invalid request")
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), tc.want) || strings.Contains(string(out), "flag provided but not defined") {
				t.Fatalf("helper was not dispatched early: output=%q err=%v", out, err)
			}
		})
	}
}

// Exercise every worker protocol operation through the GUI entry point. The
// registry check fails if a future built-in worker is added without a fixture.
func TestGUIFileWorkerCoversRegisteredTools(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pixel.png"), imageData.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	snap := agenttool.NewFileSnapshotRecorder()
	cases := []struct {
		tool  agentcore.AgentTool
		args  any
		want  string
		image bool
	}{
		{&agenttool.ReadTool{Root: root}, map[string]string{"path": "hello.txt"}, "before", false},
		{&agenttool.GrepTool{Root: root}, map[string]string{"pattern": "before"}, "hello.txt", false},
		{&agenttool.FindTool{Root: root}, map[string]string{"glob": "hello.txt"}, "hello.txt", false},
		{&agenttool.LsTool{Root: root}, map[string]string{}, "hello.txt", false},
		{&agenttool.ViewImageTool{Root: root}, map[string]string{"path": "pixel.png"}, "Viewed image", true},
		{&agenttool.ApplyPatchTool{Root: root, Snap: snap}, map[string]string{
			"patch": "*** Begin Patch\n*** Update File: hello.txt\n@@\n-before\n+after\n*** End Patch\n"}, "Applied 1 change", false},
	}
	covered := make(map[string]bool, len(cases))
	_, rgErr := exec.LookPath("rg")
	for _, tc := range cases {
		name := tc.tool.Name()
		covered[name] = true
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			tool := &toolworker.Tool{Original: tc.tool, Root: root, Snap: snap, Runner: workerTestRunner{}, Mode: permissions.New(permissions.Auto)}
			result, err := tool.Execute(context.Background(), "test", raw, nil)
			text := agentcore.ContentToText(result.Content)
			if rgErr != nil && (name == "grep" || name == "find") {
				if err != nil || !result.IsError || !strings.Contains(text, "ripgrep") {
					t.Fatalf("missing ripgrep was not reported by the worker: result=%q err=%v", text, err)
				}
				return
			}
			if err != nil || result.IsError || !strings.Contains(text, tc.want) {
				t.Fatalf("result=%q err=%v", text, err)
			}
			if tc.image {
				found := false
				for _, block := range result.Content {
					if img, ok := block.(agentcore.ImageContent); ok && img.MimeType == "image/png" && img.Data != "" {
						found = true
					}
				}
				if !found {
					t.Fatal("worker lost the image content in its response")
				}
			}
		})
	}
	for _, tool := range run.BuiltinTools(root, false) {
		if _, ok := tool.(*toolworker.Tool); ok && !covered[tool.Name()] {
			t.Errorf("GUI worker is missing a regression fixture for built-in %q", tool.Name())
		}
	}
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
