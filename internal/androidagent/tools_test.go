package androidagent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/permissions"
)

func fakeLookup(paths map[string]string) func(string) (string, error) {
	return func(name string) (string, error) {
		if path := paths[name]; path != "" {
			return path, nil
		}
		return "", exec.ErrNotFound
	}
}

func allowReadOnly(_ context.Context, _ agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
	return nil
}

func hasTool(s *Toolset, name string) bool {
	_, ok := s.Batch.Registry.Get(name)
	return ok
}

func TestCapabilitiesSelectCatalogAndPrompt(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		paths                 map[string]string
		wantRG, wantShell     bool
		wantNotes, absentNote []string
	}{
		{
			name:      "system fallback",
			paths:     map[string]string{"/system/bin/sh": "/system/bin/sh", "/system/bin/grep": "/system/bin/grep", "/system/bin/find": "/system/bin/find"},
			wantShell: true,
			wantNotes: []string{"dedicated grep and find tools are not exposed", "/system/bin/grep", "/system/bin/find"},
		},
		{
			name:      "ripgrep",
			paths:     map[string]string{"rg": "/example/rg", "/system/bin/sh": "/system/bin/sh"},
			wantRG:    true,
			wantShell: true,
			wantNotes: []string{"grep and find tools are available through ripgrep"},
		},
		{
			name:       "no shell or ripgrep",
			paths:      map[string]string{},
			wantNotes:  []string{"Ripgrep and the shell are unavailable", "Do not claim code search is available"},
			absentNote: []string{"use /system/bin/grep"},
		},
		{
			name:      "partial system search",
			paths:     map[string]string{"/system/bin/sh": "/system/bin/sh", "/system/bin/find": "/system/bin/find"},
			wantShell: true,
			wantNotes: []string{"System grep was not found", "/system/bin/find"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := newToolset(Config{Root: t.TempDir(), BeforeToolCall: allowReadOnly}, fakeLookup(tc.paths))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			for _, name := range []string{"read", "ls", "apply_patch"} {
				if !hasTool(s, name) {
					t.Errorf("missing %s", name)
				}
			}
			if hasTool(s, "grep") != tc.wantRG || hasTool(s, "find") != tc.wantRG {
				t.Errorf("ripgrep catalog: grep=%t find=%t", hasTool(s, "grep"), hasTool(s, "find"))
			}
			if hasTool(s, "bash") != tc.wantShell || hasTool(s, "write_stdin") != tc.wantShell {
				t.Errorf("shell catalog: bash=%t write_stdin=%t", hasTool(s, "bash"), hasTool(s, "write_stdin"))
			}
			if s.Permissions.Mode() != permissions.Auto {
				t.Errorf("default permission mode = %v, want auto", s.Permissions.Mode())
			}
			prompt := s.Prompt()
			for _, want := range append(tc.wantNotes, "not a workspace-level shell sandbox", "per-command network isolation is not provided") {
				if !strings.Contains(prompt, want) {
					t.Errorf("prompt missing %q: %s", want, prompt)
				}
			}
			for _, unwanted := range tc.absentNote {
				if strings.Contains(prompt, unwanted) {
					t.Errorf("prompt should not contain %q: %s", unwanted, prompt)
				}
			}
		})
	}
}

func TestRequiresWorkspaceAndHostGate(t *testing.T) {
	root := t.TempDir()
	if _, err := newToolset(Config{Root: root}, fakeLookup(nil)); err == nil {
		t.Fatal("missing host gate was accepted")
	}
	if _, err := newToolset(Config{BeforeToolCall: allowReadOnly}, fakeLookup(nil)); err == nil {
		t.Fatal("empty workspace was accepted")
	}
	if _, err := newToolset(Config{Root: filepath.Join(root, "missing"), BeforeToolCall: allowReadOnly}, fakeLookup(nil)); err == nil {
		t.Fatal("missing workspace was accepted")
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newToolset(Config{Root: file, BeforeToolCall: allowReadOnly}, fakeLookup(nil)); err == nil {
		t.Fatal("file as workspace was accepted")
	}
	if runtime.GOOS != "android" {
		if _, err := New(Config{Root: root, BeforeToolCall: allowReadOnly}); err == nil {
			t.Fatal("Android entry point accepted a desktop host")
		}
	}
}

func TestNoShellOmitsShellCatalogAndPrompt(t *testing.T) {
	s, err := newToolset(Config{
		Root: t.TempDir(), BeforeToolCall: allowReadOnly, NoShell: true,
	}, fakeLookup(map[string]string{"/system/bin/sh": "/system/bin/sh", "/system/bin/grep": "/system/bin/grep"}))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Capabilities.Shell != "" || hasTool(s, "bash") || hasTool(s, "write_stdin") {
		t.Fatalf("NoShell still exposed shell: caps=%+v", s.Capabilities)
	}
	if prompt := s.Prompt(); !strings.Contains(prompt, "shell are unavailable") {
		t.Fatalf("prompt does not say shell is unavailable: %q", prompt)
	}
}

func TestAndroidUsePromptTracksCurrentCatalog(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint string
		phase2         bool
		wantTool       bool
	}{
		{"disabled", "", false, false},
		{"visible screen", "/private/android-use.json", false, true},
		{"background enabled", "/private/android-use.json", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := newToolset(Config{
				Root: t.TempDir(), BeforeToolCall: allowReadOnly, NoShell: true,
				AndroidUseEndpoint: tc.endpoint, AndroidUsePhase2: tc.phase2,
			}, fakeLookup(nil))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if hasTool(s, "android_use") != tc.wantTool {
				t.Fatal("android_use catalog does not match endpoint")
			}
			prompt := s.Prompt()
			if strings.Contains(prompt, "AndroidUse is available through android_use") != tc.wantTool {
				t.Fatalf("prompt does not match catalog: %s", prompt)
			}
			if strings.Contains(prompt, "Background-display control is separately enabled") != tc.phase2 {
				t.Fatalf("background prompt does not match catalog: %s", prompt)
			}
		})
	}
}

func TestGitToolIsCataloguedAndNetworkFailsClosed(t *testing.T) {
	s, err := newToolset(Config{Root: t.TempDir(), BeforeToolCall: allowReadOnly}, fakeLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !hasTool(s, "git") {
		t.Fatal("git tool is missing from the Android catalog")
	}
	results, _ := agenttool.ExecuteToolCalls(context.Background(), s.Batch, []agentcore.AgentToolCall{{
		ID: "git-1", Name: "git", Arguments: json.RawMessage(`{"action":"clone","url":"https://example.invalid/repo.git"}`),
	}}, nil)
	if len(results) != 1 || !results[0].IsError || !strings.Contains(agentcore.ContentToText(results[0].Content), "network access is not authorized") {
		t.Fatalf("ungranted git clone = %+v", results)
	}
}

func TestShellFailsClosedWithoutHostGrant(t *testing.T) {
	paths := map[string]string{"/system/bin/sh": "/system/bin/sh"}
	s, err := newToolset(Config{Root: t.TempDir(), BeforeToolCall: allowReadOnly}, fakeLookup(paths))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	results, _ := agenttool.ExecuteToolCalls(context.Background(), s.Batch, []agentcore.AgentToolCall{{
		ID: "1", Name: "bash", Arguments: json.RawMessage(`{"command":"printf hello"}`),
	}}, nil)
	if len(results) != 1 || !results[0].IsError || !strings.Contains(agentcore.ContentToText(results[0].Content), "no OS sandbox runner") {
		t.Fatalf("shell without a grant did not fail closed: %+v", results)
	}
	shell, _ := s.Batch.Registry.Get("bash")
	if !strings.Contains(shell.Description(), "not a workspace-level shell sandbox") || strings.Contains(shell.Description(), "network off") {
		t.Fatalf("misleading shell description: %s", shell.Description())
	}
}

func TestHostGrantAndReadOnlyMode(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh unavailable on this host")
	}
	var gateCalls int
	gate := func(_ context.Context, call agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		gateCalls++
		if call.Name != "bash" {
			return &agentcore.BeforeToolCallDecision{Block: true}
		}
		return &agentcore.BeforeToolCallDecision{Bare: true, BareReason: "test fixture"}
	}
	mode := permissions.New(permissions.Auto)
	s, err := newToolset(Config{Root: t.TempDir(), Permissions: mode, BeforeToolCall: gate}, fakeLookup(map[string]string{"/system/bin/sh": sh}))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	calls := []agentcore.AgentToolCall{{ID: "1", Name: "bash", Arguments: json.RawMessage(`{"command":"printf hello","yield_time_ms":2000}`)}}
	results, _ := agenttool.ExecuteToolCalls(context.Background(), s.Batch, calls, nil)
	if len(results) != 1 || results[0].IsError || !strings.Contains(agentcore.ContentToText(results[0].Content), "hello") || gateCalls != 1 {
		t.Fatalf("host-approved shell: results=%+v gateCalls=%d", results, gateCalls)
	}
	mode.Set(permissions.ReadOnly)
	results, _ = agenttool.ExecuteToolCalls(context.Background(), s.Batch, calls, nil)
	if len(results) != 1 || !results[0].IsError || !strings.Contains(agentcore.ContentToText(results[0].Content), "read-only") {
		t.Fatalf("read-only shell should be blocked: %+v", results)
	}
}

func TestShellDoesNotClaimNetworkOff(t *testing.T) {
	s, err := newToolset(Config{Root: t.TempDir(), BeforeToolCall: allowReadOnly}, fakeLookup(map[string]string{"/system/bin/sh": "/system/bin/sh"}))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	off := json.RawMessage(`{"command":"printf never","network":"off"}`)
	if errs := s.Batch.Registry.Validate("bash", off); len(errs) == 0 {
		t.Fatal("network=off must not be advertised when it cannot be enforced")
	}
	shell, _ := s.Batch.Registry.Get("bash")
	result, err := shell.Execute(context.Background(), "1", off, nil)
	if err != nil || !result.IsError || !strings.Contains(agentcore.ContentToText(result.Content), "cannot enforce network=off") {
		t.Fatalf("direct network=off call = %+v, %v", result, err)
	}
}

func TestNetworkRequestNeedsHostGrant(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh unavailable on this host")
	}
	grantNetwork := false
	gate := func(_ context.Context, _ agentcore.AgentToolCall) *agentcore.BeforeToolCallDecision {
		return &agentcore.BeforeToolCallDecision{Bare: true, Network: grantNetwork, BareReason: "test fixture"}
	}
	s, err := newToolset(Config{Root: t.TempDir(), BeforeToolCall: gate}, fakeLookup(map[string]string{"/system/bin/sh": sh}))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	calls := []agentcore.AgentToolCall{{ID: "1", Name: "bash", Arguments: json.RawMessage(`{"command":"printf local","network":"on","yield_time_ms":2000}`)}}
	results, _ := agenttool.ExecuteToolCalls(context.Background(), s.Batch, calls, nil)
	if len(results) != 1 || !results[0].IsError || !strings.Contains(agentcore.ContentToText(results[0].Content), "network access was requested but was not authorized") {
		t.Fatalf("network request without grant should fail: %+v", results)
	}
	grantNetwork = true
	results, _ = agenttool.ExecuteToolCalls(context.Background(), s.Batch, calls, nil)
	if len(results) != 1 || results[0].IsError || !strings.Contains(agentcore.ContentToText(results[0].Content), "local") {
		t.Fatalf("host-granted network request should execute: %+v", results)
	}
}

func TestExecutableLookupErrorsDoNotClaimCapabilities(t *testing.T) {
	s, err := newToolset(Config{Root: t.TempDir(), BeforeToolCall: allowReadOnly}, func(string) (string, error) {
		return "", errors.New("permission denied")
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if hasTool(s, "grep") || hasTool(s, "find") || hasTool(s, "bash") {
		t.Fatalf("lookup failure exposed unavailable tools: %+v", s.Capabilities)
	}
}
