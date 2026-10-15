package toolworker

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/grants"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/seatbelt"
)

func workerBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "golder-worker")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/golder")
	cmd.Dir = filepath.Join("..", "..")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build worker binary: %v\n%s", err, out)
	}
	return bin
}

func TestGrantSnapshotCarriesCapabilitiesAcrossJSON(t *testing.T) {
	in := grants.Entries{
		Remembered: []grants.Grant{{Path: "/remembered", Allow: grants.ReadWrite, Scope: grants.Subtree, Lifetime: grants.Remembered}},
		Session:    []grants.Grant{{Path: "/session/file", Deny: grants.Read, Scope: grants.Exact, Lifetime: grants.Session}},
	}
	data, err := json.Marshal(Request{Grants: in, Rows: grantRows(in)})
	if err != nil {
		t.Fatal(err)
	}
	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		t.Fatal(err)
	}
	got := restoreGrantRows(req.Rows)
	if len(got.Remembered) != 1 || !got.Remembered[0].Allow.Has(grants.Write) ||
		len(got.Session) != 1 || !got.Session[0].Deny.Has(grants.Read) || got.Session[0].Scope != grants.Exact {
		t.Errorf("authorization snapshot lost capabilities or scope: %+v (wire %s)", got, data)
	}
}

func TestIsolatedFileToolsAndRewind(t *testing.T) {
	if runtime.GOOS != "darwin" || !seatbelt.Available() {
		t.Skip("integration test requires macOS sandbox-exec")
	}
	root := t.TempDir()
	bin := workerBinary(t)
	mode := permissions.New(permissions.Auto)
	runner := seatbelt.New(root).(seatbelt.CallRunner)
	snap := agenttool.NewFileSnapshotRecorder()
	file := filepath.Join(root, "hello.txt")
	if err := os.WriteFile(file, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		tool agentcore.AgentTool
		args any
		want string
	}{
		{"read", &agenttool.ReadTool{Root: root}, map[string]string{"path": "hello.txt"}, "before"},
		{"apply_patch", &agenttool.ApplyPatchTool{Root: root, Snap: snap}, map[string]string{
			"patch": "*** Begin Patch\n*** Update File: hello.txt\n@@\n-before\n+after\n*** End Patch\n"}, "Applied 1 change"},
	} {
		args, _ := json.Marshal(c.args)
		tool := &Tool{Original: c.tool, Root: root, Snap: snap, Runner: runner, Mode: mode, Binary: bin}
		result, err := tool.Execute(context.Background(), "test", args, nil)
		if err != nil || !strings.Contains(agentcore.ContentToText(result.Content), c.want) {
			t.Fatalf("%s: result=%q err=%v", c.name, agentcore.ContentToText(result.Content), err)
		}
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != "after\n" {
		t.Fatalf("patch content = %q, err=%v", got, err)
	}
	if !snap.Commit("leaf", "test") {
		t.Fatal("worker did not transmit rewind snapshots")
	}
	if _, _, warns, err := snap.Restore(0); err != nil || len(warns) > 0 {
		t.Fatalf("rewind: %v, warnings=%v", err, warns)
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != "before\n" {
		t.Fatalf("rewound content = %q, err=%v", got, err)
	}
}

func TestFileReadGrantCrossesWorkerBoundary(t *testing.T) {
	if runtime.GOOS != "darwin" || !seatbelt.Available() {
		t.Skip("integration test requires macOS sandbox-exec")
	}
	root := t.TempDir()
	outside := t.TempDir()
	file := filepath.Join(outside, "consented.txt")
	if err := os.WriteFile(file, []byte("outside but granted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := workerBinary(t)
	base := &Tool{Original: &agenttool.ReadTool{Root: root}, Root: root,
		Runner: seatbelt.New(root), Mode: permissions.New(permissions.Auto), Binary: bin}
	args, _ := json.Marshal(map[string]string{"path": file})
	result, err := base.Execute(context.Background(), "no-grant", args, nil)
	if err != nil || !strings.Contains(agentcore.ContentToText(result.Content), "outside the workspace root") {
		t.Fatalf("ungranted outside read was not refused: %q (err=%v)", agentcore.ContentToText(result.Content), err)
	}
	result, err = base.Execute(agentcore.WithReadGrant(context.Background(), file), "one-call", args, nil)
	if err != nil || !strings.Contains(agentcore.ContentToText(result.Content), "outside but granted") {
		t.Fatalf("exact file grant not honored inside worker: result=%q err=%v", agentcore.ContentToText(result.Content), err)
	}
}
