package agenttool

import (
	"context"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/toolchain"
)

func TestBashProfileNeedsHostGrantAndRevalidatesBinding(t *testing.T) {
	p := toolchain.Plan{Name: "go", Binding: "bound", Executable: "/usr/local/go/bin/go", ReadDirs: []string{"/cache/modules"}, WriteDirs: []string{"/cache/build"}}
	runner := &stubRunner{}
	tool := &BashTool{Dir: t.TempDir(), Sandbox: runner, Permissions: permissions.New(permissions.Auto), ResolveToolchain: func(context.Context, string, string) (toolchain.Plan, error) { return p, nil }}
	args := map[string]any{"command": "echo ok", "profile": "go"}
	r, err := runBashCtx(t, context.Background(), tool, args, nil)
	if err != nil || !r.IsError || runner.calls != 0 {
		t.Fatal("model argument manufactured a grant")
	}
	ctx := agentcore.WithToolchainGrant(context.Background(), agentcore.ToolchainGrant{Name: p.Name, Binding: p.Binding, Executable: p.Executable, ReadDirs: p.ReadDirs, WriteDirs: p.WriteDirs})
	r, err = runBashCtx(t, ctx, tool, args, nil)
	if err != nil || r.IsError || runner.calls != 1 || runner.policy.Network || len(runner.policy.WriteDirs) != 1 || runner.policy.WriteDirs[0] != "/cache/build" {
		t.Fatalf("scoped execution: %+v %v %+v", r, err, runner.policy)
	}
	p.Binding = "changed"
	r, err = runBashCtx(t, ctx, tool, args, nil)
	if err != nil || !r.IsError || runner.calls != 1 || r.Details.(map[string]any)["execution_reason"] != "profile_changed" {
		t.Fatalf("changed profile ran: %+v %v", r, err)
	}
	tool.StopSessions()
}
