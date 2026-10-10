package agenttool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/permissions"
)

// runWithCtx executes a tool with the given context, so a test can carry the
// one-call read grant the permission layer publishes.
func runWithCtx(t *testing.T, ctx context.Context, tool agentcore.AgentTool, args map[string]any) agentcore.AgentToolResult {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	res, gerr := tool.Execute(ctx, "call-1", raw, nil)
	if gerr != nil {
		t.Fatalf("execute returned go error: %v", gerr)
	}
	return res
}

// grantFixture builds a workspace and an outside directory holding a text file
// and a matching line for the search tools.
func grantFixture(t *testing.T) (workspace, outside, target string) {
	t.Helper()
	workspace, outside = t.TempDir(), t.TempDir()
	target = filepath.Join(outside, "notes.txt")
	if err := os.WriteFile(target, []byte("needle here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { permissions.SetReadRoots(nil) })
	return workspace, outside, target
}

// TestReadGrantAllowsOneCall pins the "just this read" answer: with the grant
// in the context the read succeeds, and without it the same path is still
// refused — the grant lives exactly as long as the call it was issued for.
func TestReadGrantAllowsOneCall(t *testing.T) {
	workspace, _, target := grantFixture(t)
	tool := &ReadTool{Root: workspace}

	denied := resultText(runWithCtx(t, context.Background(), tool, map[string]any{"path": target}))
	if !strings.Contains(denied, "outside the workspace root") {
		t.Fatalf("without a grant the read must be refused, got %q", denied)
	}

	ctx := agentcore.WithReadGrant(context.Background(), target)
	text := resultText(runWithCtx(t, ctx, tool, map[string]any{"path": target}))
	if !strings.Contains(text, "needle here") {
		t.Fatalf("a granted read must return the file, got %q", text)
	}
}

// TestReadGrantIsScopedToItsPath pins the size of the one-call answer: the
// grant names one path, so a sibling outside it is still refused.
func TestReadGrantIsScopedToItsPath(t *testing.T) {
	workspace, outside, target := grantFixture(t)
	other := filepath.Join(outside, "other.txt")
	if err := os.WriteFile(other, []byte("other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := agentcore.WithReadGrant(context.Background(), target)
	text := resultText(runWithCtx(t, ctx, &ReadTool{Root: workspace}, map[string]any{"path": other}))
	if !strings.Contains(text, "outside the workspace root") {
		t.Fatalf("a grant for one file must not allow its sibling, got %q", text)
	}
}

// TestSessionGrantAllowsReadAndSearch covers the directory answer for the read
// tool and both search tools, since a session grant is checked by the shared
// resolver rather than by one tool.
func TestSessionGrantAllowsReadAndSearch(t *testing.T) {
	requireRG(t)
	workspace, outside, target := grantFixture(t)
	if _, err := permissions.AddReadRoot(outside); err != nil {
		t.Fatalf("AddReadRoot: %v", err)
	}

	if text := resultText(runWithCtx(t, context.Background(), &ReadTool{Root: workspace}, map[string]any{"path": target})); !strings.Contains(text, "needle here") {
		t.Errorf("read under a session grant should succeed: %q", text)
	}
	if text := resultText(runWithCtx(t, context.Background(), &LsTool{Root: workspace}, map[string]any{"path": outside})); !strings.Contains(text, "notes.txt") {
		t.Errorf("ls under a session grant should succeed: %q", text)
	}
	grepText := resultText(runWithCtx(t, context.Background(), &GrepTool{Root: workspace}, map[string]any{"pattern": "needle", "path": outside}))
	if !strings.Contains(grepText, "needle here") {
		t.Errorf("grep under a session grant should search the directory: %q", grepText)
	}
	findText := resultText(runWithCtx(t, context.Background(), &FindTool{Root: workspace}, map[string]any{"glob": "*.txt", "path": outside}))
	if !strings.Contains(findText, "notes.txt") {
		t.Errorf("find under a session grant should list the directory: %q", findText)
	}
}

// TestGrepUnderGrantReportsRelativePaths pins the result shape for a search
// rooted at a granted directory: paths are relative to what was searched, not
// a chain of "../..", which is what rgScope exists to produce.
func TestGrepUnderGrantReportsRelativePaths(t *testing.T) {
	requireRG(t)
	workspace, outside, _ := grantFixture(t)
	if _, err := permissions.AddReadRoot(outside); err != nil {
		t.Fatalf("AddReadRoot: %v", err)
	}
	text := resultText(runWithCtx(t, context.Background(), &GrepTool{Root: workspace}, map[string]any{"pattern": "needle", "path": outside}))
	if strings.Contains(text, "..") {
		t.Errorf("a granted search should not climb out with ..: %q", text)
	}
	if !strings.Contains(text, "notes.txt:1:") {
		t.Errorf("expected a directory-relative match, got %q", text)
	}
}

// TestCredentialPathStillRefusedUnderGrant is the safety regression: a grant
// does not open credential material, even when the granted directory contains
// it. The static floor refuses the raw argument and this refuses the resolved
// path, so neither spelling gets through.
func TestCredentialPathStillRefusedUnderGrant(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace := t.TempDir()
	ssh := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(ssh, 0o755); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(ssh, "id_rsa")
	if err := os.WriteFile(key, []byte("PRIVATE KEY\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { permissions.SetReadRoots(nil) })
	if _, err := permissions.AddReadRoot(home); err != nil {
		// $HOME itself is refused by design, so grant the directory above the
		// credential one instead: the point is that a grant covering the path
		// still does not open it.
		if _, err := permissions.AddReadRoot(ssh); err != nil {
			t.Fatalf("AddReadRoot: %v", err)
		}
	}

	text := resultText(runWithCtx(t, context.Background(), &ReadTool{Root: workspace}, map[string]any{"path": key}))
	if strings.Contains(text, "PRIVATE KEY") {
		t.Fatal("a read grant must never expose credential material")
	}
	if !strings.Contains(text, "credential material") {
		t.Errorf("expected a credential refusal, got %q", text)
	}
}

// TestApplyPatchIgnoresReadGrants is the write-side safety regression: a read
// grant answers a question about reading, so the write path must keep refusing
// an out-of-workspace target even when the context carries one, even when the
// session has granted the directory for reading, and even when both are true.
func TestApplyPatchIgnoresReadGrants(t *testing.T) {
	workspace, outside, _ := grantFixture(t)
	if _, err := permissions.AddReadRoot(outside); err != nil {
		t.Fatalf("AddReadRoot: %v", err)
	}
	target := filepath.Join(outside, "written.txt")
	patchText := "*** Begin Patch\n*** Add File: " + target + "\n+oops\n*** End Patch\n"
	args, err := json.Marshal(map[string]string{"patch": patchText})
	if err != nil {
		t.Fatal(err)
	}
	ctx := agentcore.WithReadGrant(context.Background(), target)
	res, gerr := (&ApplyPatchTool{Root: workspace}).Execute(ctx, "call-1", args, nil)
	if gerr != nil {
		t.Fatalf("execute returned go error: %v", gerr)
	}
	if txt := resultText(res); !strings.Contains(txt, "outside the workspace root") {
		t.Fatalf("apply_patch must not honour a read grant, got %q", txt)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("a rejected patch must not write outside the workspace")
	}
}
