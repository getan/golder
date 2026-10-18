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

func TestWithEnvelopePreservesDetails(t *testing.T) {
	res := withEnvelope(agentcore.AgentToolResult{Details: map[string]any{"files": 2}}, true)
	details, ok := res.Details.(map[string]any)
	if !ok || details["files"] != 2 || details["envelope"] != "sandbox" {
		t.Fatalf("sandboxed details = %#v, want files=2 envelope=sandbox", res.Details)
	}
	res = withEnvelope(agentcore.AgentToolResult{}, false)
	details, ok = res.Details.(map[string]any)
	if !ok || details["envelope"] != "bare" {
		t.Fatalf("in-process details = %#v, want envelope=bare", res.Details)
	}
}

// A session grant for a credential path used to fail every worker call with
// "grant targets protected state or credential material", because the policy
// check rejected the whole request. The path must be dropped from the mounts,
// while ordinary grants on the same call still apply.
func TestWorkerPolicyDropsAmbientProtectedGrant(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	secret := filepath.Join(home, ".ssh")
	work := t.TempDir()
	entries := grants.Entries{Session: []grants.Grant{
		{Path: secret, Allow: grants.Read, Scope: grants.Subtree, Lifetime: grants.Session},
		{Path: work, Allow: grants.Read, Scope: grants.Subtree, Lifetime: grants.Session},
	}}
	store, err := grants.FromSnapshot(entries)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	grants.SetProcess(store)
	defer grants.SetProcess(nil)

	policy, err := workerPolicy(Request{Grants: entries}, nil, "/bin/true")
	if err != nil {
		t.Fatalf("ambient protected grant must not fail the call: %v", err)
	}
	for _, p := range policy.ReadDirs {
		if p == secret {
			t.Errorf("protected path %q was mounted into the worker", p)
		}
	}
	found := false
	for _, p := range policy.ReadDirs {
		if p == work {
			found = true
		}
	}
	if !found {
		t.Errorf("ordinary grant %q missing from mounts: %+v", work, policy.ReadDirs)
	}
}

// An explicit one-call grant for a protected path is still refused: that is the
// caller asking for this exact file, not an ambient grant it did not choose.
func TestWorkerPolicyRefusesExplicitProtectedRead(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	_, err = workerPolicy(Request{ReadFile: filepath.Join(home, ".ssh", "id_ed25519")}, nil, "/bin/true")
	if err == nil || !strings.Contains(err.Error(), "protected state or credential material") {
		t.Fatalf("explicit protected read = %v, want the protected-path refusal", err)
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
