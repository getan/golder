package agenttool

// Tests for the write side's consent path, mirroring the read-grant tests: the
// gate asks the same question about an out-of-workspace write as about a read,
// so the tool that writes must honour the same answers. Before this existed an
// out-of-workspace patch was refused with no prompt and no grant at all.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/grants"
)

// patchFor builds the single argument an apply_patch call takes: one file, added.
func patchFor(t *testing.T, path string) json.RawMessage {
	t.Helper()
	text := "*** Begin Patch\n*** Add File: " + path + "\n+written by the test\n*** End Patch\n"
	raw, err := json.Marshal(map[string]string{"patch": text})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// runPatchAt executes one apply_patch call that adds path, returning its
// rendered result. The name is distinct from runPatch in
// apply_patch_tool_test.go, which takes the patch text itself.
func runPatchAt(t *testing.T, ctx context.Context, tool *ApplyPatchTool, path string) string {
	t.Helper()
	res, gerr := tool.Execute(ctx, "call-1", patchFor(t, path), nil)
	if gerr != nil {
		t.Fatalf("execute returned go error: %v", gerr)
	}
	return resultText(res)
}

// writeGrantFixture returns a workspace, an outside directory, a process-wide
// authorization table, and the goal path — the write side of grantFixture.
func writeGrantFixture(t *testing.T) (workspace, outside, target string, store *grants.Store) {
	t.Helper()
	workspace, outside, _ = grantFixture(t)
	target = filepath.Join(outside, "written.txt")
	store, err := grants.New(grants.Options{Path: filepath.Join(t.TempDir(), "grants.json")})
	if err != nil {
		t.Fatalf("grants.New: %v", err)
	}
	grants.SetProcess(store)
	t.Cleanup(func() { grants.SetProcess(nil) })
	return workspace, outside, target, store
}

// TestWriteGrantAllowsOneCall pins the "allow this write once" answer: the grant
// lives exactly as long as the call it was issued for.
func TestWriteGrantAllowsOneCall(t *testing.T) {
	workspace, _, target, _ := writeGrantFixture(t)
	tool := &ApplyPatchTool{Root: workspace}

	denied := runPatchAt(t, context.Background(), tool, target)
	if !strings.Contains(denied, "outside the workspace root") {
		t.Fatalf("without a grant the write must be refused, got %q", denied)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("a refused patch must not write")
	}

	ctx := agentcore.WithWriteGrant(context.Background(), target)
	text := runPatchAt(t, ctx, tool, target)
	if !strings.Contains(text, "Applied 1 change") {
		t.Fatalf("a granted write must apply, got %q", text)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("the granted write did not land: %v", err)
	}
}

// TestWriteGrantIsScopedToItsPath pins the size of the one-call answer: it names
// one path, so a sibling outside it is still refused.
func TestWriteGrantIsScopedToItsPath(t *testing.T) {
	workspace, outside, granted, _ := writeGrantFixture(t)
	other := filepath.Join(outside, "other.txt")

	ctx := agentcore.WithWriteGrant(context.Background(), granted)
	text := runPatchAt(t, ctx, &ApplyPatchTool{Root: workspace}, other)
	if !strings.Contains(text, "outside the workspace root") {
		t.Fatalf("a grant for one file must not allow its sibling, got %q", text)
	}
}

// TestStandingWriteGrantAllowsPatch covers the directory answer: once the table
// carries a write grant for the directory, the next patch needs no grant on its
// context. This is what makes "allow writing <dir> for this session" mean
// anything to the tool.
func TestStandingWriteGrantAllowsPatch(t *testing.T) {
	workspace, outside, target, store := writeGrantFixture(t)

	if _, err := store.Add(grants.Grant{
		Path: outside, Allow: grants.Write, Scope: grants.Subtree, Lifetime: grants.Session,
	}); err != nil {
		t.Fatalf("granting write: %v", err)
	}
	text := runPatchAt(t, context.Background(), &ApplyPatchTool{Root: workspace}, target)
	if !strings.Contains(text, "Applied 1 change") {
		t.Fatalf("a standing write grant must allow the patch, got %q", text)
	}
}

// TestStandingReadGrantDoesNotAllowAPatch is the safety half, and the reason the
// table keeps the capabilities apart: a directory the user allowed for reading
// is not thereby writable. TestApplyPatchIgnoresReadGrants pins the one-call
// form of the same rule.
func TestStandingReadGrantDoesNotAllowAPatch(t *testing.T) {
	workspace, outside, target, store := writeGrantFixture(t)

	if _, err := store.Add(grants.Grant{
		Path: outside, Allow: grants.Read, Scope: grants.Subtree, Lifetime: grants.Session,
	}); err != nil {
		t.Fatalf("granting read: %v", err)
	}
	text := runPatchAt(t, context.Background(), &ApplyPatchTool{Root: workspace}, target)
	if !strings.Contains(text, "outside the workspace root") {
		t.Fatalf("a read grant must not authorize a write, got %q", text)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("a refused patch must not write")
	}
}

// TestWritableAtAllowsPatch covers the third answer: the sandbox's writable-root
// registry, which config.toml, permissions.toml and every escalation answer
// feed. It is injected, so a tool with none is left with the table's grants —
// the stricter reading.
func TestWritableAtAllowsPatch(t *testing.T) {
	workspace, _, target, _ := writeGrantFixture(t)
	tool := &ApplyPatchTool{Root: workspace, WritableAt: func(string) bool { return true }}

	text := runPatchAt(t, context.Background(), tool, target)
	if !strings.Contains(text, "Applied 1 change") {
		t.Fatalf("an already-writable path must be patchable, got %q", text)
	}
}

// TestWriteRefusesSecretMaterialEvenWhenWritable is the floor under every write
// answer: an SSH identity directory is refused even when the registries and the
// table would both allow the write. The check runs before them, and both
// spellings are checked, so neither a literal path nor a link reaching one gets
// through.
func TestWriteRefusesSecretMaterialEvenWhenWritable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace := t.TempDir()
	ssh := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(ssh, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(ssh, "authorized_keys")

	tool := &ApplyPatchTool{Root: workspace, WritableAt: func(string) bool { return true }}
	text := runPatchAt(t, context.Background(), tool, target)
	if strings.Contains(text, "Applied 1 change") {
		t.Fatalf("a patch must never rewrite live secret material, got %q", text)
	}
	if !strings.Contains(text, "secret") {
		t.Errorf("the refusal should name the reason, got %q", text)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("a refused patch must not write")
	}
}
