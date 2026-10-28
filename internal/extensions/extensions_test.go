package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func testManager(t *testing.T, ref SourceRef, c SourceConfig, allow func(string) bool) *Manager {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GOLDER_HOME", home)
	path := filepath.Join(home, "config.toml")
	if e := WriteSource(path, ref, &c); e != nil {
		t.Fatal(e)
	}
	m, e := New(Options{ConfigPath: path, Cwd: t.TempDir(), Allow: allow})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func TestConfigPreservesOtherTablesAndMultipleSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	original := "# keep my comment\nmodel = 'example'\n[memory]\nenabled = false\n"
	if e := os.WriteFile(path, []byte(original), 0o600); e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"one", "two"} {
		if e := WriteSource(path, SourceRef{Kind: "mcp", ID: id}, &SourceConfig{Transport: "http", URL: "https://example.com/mcp", Headers: map[string]string{"Authorization": "${credential:private}"}}); e != nil {
			t.Fatal(e)
		}
	}
	c, e := LoadConfig(path)
	if e != nil {
		t.Fatal(e)
	}
	one := c.MCP.Servers["one"]
	one.Enabled = true
	if e = WriteSource(path, SourceRef{Kind: "mcp", ID: "one"}, &one); e != nil {
		t.Fatal(e)
	}
	if e = WriteSource(path, SourceRef{Kind: "mcp", ID: "two"}, nil); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), original) {
		t.Fatalf("unrelated settings changed:\n%s", b)
	}
	c, e = LoadConfig(path)
	if e != nil || len(c.MCP.Servers) != 1 || !c.MCP.Servers["one"].Enabled {
		t.Fatalf("bad round trip: %+v %v", c, e)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatal(info.Mode())
	}
}

func TestNameMappingAndSchemaBoundary(t *testing.T) {
	ref := SourceRef{Kind: "mcp", ID: "team-docs"}
	a := ModelName(ref, "foo.bar")
	b := ModelName(ref, "foo_bar")
	if a == b || len(a) > 64 || a != ModelName(ref, "foo.bar") {
		t.Fatalf("unsafe names: %s %s", a, b)
	}
	if len(ModelName(ref, strings.Repeat("tool", 100))) > 64 {
		t.Fatal("oversized function name")
	}
	for _, schema := range []string{`{"type":"string"}`, `{"type":"object","$ref":"https://127.0.0.1/x"}`, `{"type":"object","properties":{"x":{"$dynamicRef":"file:///etc/passwd"}}}`} {
		if _, e := compileSchema(json.RawMessage(schema)); e == nil {
			t.Fatal("accepted", schema)
		}
	}
	if _, e := compileSchema(json.RawMessage(`{"type":"object","$defs":{"s":{"type":"string"}},"properties":{"x":{"$ref":"#/$defs/s"}}}`)); e != nil {
		t.Fatal(e)
	}
}

func TestArgvIsNotShellAndHasFixedExecutable(t *testing.T) {
	cmd := cliToolSpec{Command: []string{"echo", "--text", "{{text}}", "{{object}}"}}
	got, e := renderArgv(cmd, json.RawMessage(`{"text":"-rf ; $(touch /tmp/no)","object":{"x":1}}`))
	if e != nil {
		t.Fatal(e)
	}
	if len(got) != 4 || got[2] != "-rf ; $(touch /tmp/no)" || got[3] != `{"x":1}` {
		t.Fatalf("%q", got)
	}
	for _, argv := range [][]string{{"{{program}}"}, {"echo", "prefix{{text}}"}} {
		if _, e := renderArgv(cliToolSpec{Command: argv}, json.RawMessage(`{"text":"x","program":"sh"}`)); e == nil {
			t.Fatal("accepted unsafe placeholder")
		}
	}
}

func TestEnvironmentAndBoundedTerminalData(t *testing.T) {
	t.Setenv("EXAMPLE_TOKEN", "do-not-inherit")
	t.Setenv("GOLDER_PRIVATE", "private")
	env, e := processEnv(SourceConfig{}, "/private/home")
	if e != nil {
		t.Fatal(e)
	}
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "do-not-inherit") || strings.Contains(joined, "GOLDER_PRIVATE") {
		t.Fatal("secret inherited")
	}
	env, e = processEnv(SourceConfig{EnvPassthrough: []string{"EXAMPLE_TOKEN"}}, "/private/home")
	if e != nil || !strings.Contains(strings.Join(env, "\n"), "EXAMPLE_TOKEN=do-not-inherit") {
		t.Fatal("explicit env missing", e)
	}
	b := &logBuffer{limit: 32}
	_, _ = b.Write([]byte(strings.Repeat("x", 10000)))
	if len(b.data) != 32 || !b.truncated {
		t.Fatal("unbounded output")
	}
	if s := clean("ok\x1b[31mred\x1b[0m\x1b]52;c;secret\aend", 1024); s != "okredend" {
		t.Fatalf("escape sequence survived %q", s)
	}
}

func fixtureServer(calls *atomic.Int64) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	s.AddTool(&mcp.Tool{Name: "echo", Description: "Echo", InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`)}, func(_ context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(r.Params.Arguments)}}, StructuredContent: map[string]any{"count": 1}}, nil
	})
	s.AddPrompt(&mcp.Prompt{Name: "summary"}, func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: "Summarize this"}}}}, nil
	})
	s.AddResource(&mcp.Resource{Name: "notes", URI: "notes://fixture"}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "notes://fixture", Text: "verified notes"}}}, nil
	})
	return s
}

func TestMCPHTTPDiscoveryGateResultAndRevocation(t *testing.T) {
	var calls, requests atomic.Int64
	s := fixtureServer(&calls)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{Stateless: true})
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); handler.ServeHTTP(w, r) }))
	t.Cleanup(httpServer.Close)
	ref := SourceRef{Kind: "mcp", ID: "fixture"}
	m := testManager(t, ref, SourceConfig{Enabled: true, Transport: "http", URL: httpServer.URL}, nil)
	if requests.Load() != 0 || len(m.Tools()) != 0 {
		t.Fatal("discovery connected without consent")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if e := m.Connect(ctx, ref); e == nil {
		t.Fatal("connected unapproved source")
	}
	if e := m.Approve(ref); e != nil {
		t.Fatal(e)
	}
	if e := m.Connect(ctx, ref); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 0 {
		t.Fatal("probe called a business tool")
	}
	tools := m.Tools()
	if len(tools) != 1 {
		t.Fatalf("tools: %+v", m.List())
	}
	r, e := tools[0].Execute(ctx, "one", json.RawMessage(`{"text":"hello"}`), nil)
	if e != nil || !r.IsError || calls.Load() != 0 {
		t.Fatal("ungated execution")
	}
	approved := agentcore.WithNetworkGrant(agentcore.WithExternalToolAuthorization(ctx))
	r, e = tools[0].Execute(approved, "two", json.RawMessage(`{"text":"hello"}`), nil)
	if e != nil || r.IsError || r.Details == nil || calls.Load() != 1 {
		t.Fatalf("call: %+v %v %d", r, e, calls.Load())
	}
	preview, e := m.Prompt(ctx, ref, "summary", nil)
	if e != nil || !strings.Contains(preview, "Summarize") {
		t.Fatal(preview, e)
	}
	preview, e = m.Resource(ctx, ref, "notes://fixture")
	if e != nil || !strings.Contains(preview, "verified") {
		t.Fatal(preview, e)
	}
	// A persisted, matching directory can be advertised next session without I/O.
	before := requests.Load()
	next, e := New(m.opts)
	if e != nil {
		t.Fatal(e)
	}
	if len(next.Tools()) != 1 || requests.Load() != before {
		t.Fatal("cache caused I/O or lost catalog")
	}
	_ = next.Close()
	if e = m.Disable(ref); e != nil {
		t.Fatal(e)
	}
	r, e = tools[0].Execute(approved, "three", json.RawMessage(`{"text":"hello"}`), nil)
	if e != nil || !r.IsError || calls.Load() != 1 {
		t.Fatal("stale tool survived revocation")
	}
}

func TestNoReplayOnMCPFailure(t *testing.T) {
	var calls atomic.Int64
	s := mcp.NewServer(&mcp.Implementation{Name: "failure", Version: "1"}, nil)
	s.AddTool(&mcp.Tool{Name: "mutate", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		return nil, errors.New("failed after possible mutation")
	})
	h := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{Stateless: true}))
	t.Cleanup(h.Close)
	ref := SourceRef{Kind: "mcp", ID: "failure"}
	m := testManager(t, ref, SourceConfig{Enabled: true, Transport: "http", URL: h.URL}, nil)
	if e := m.Approve(ref); e != nil {
		t.Fatal(e)
	}
	if e := m.Connect(context.Background(), ref); e != nil {
		t.Fatal(e)
	}
	ctx := agentcore.WithNetworkGrant(agentcore.WithExternalToolAuthorization(context.Background()))
	r, e := m.Tools()[0].Execute(ctx, "mutate", json.RawMessage(`{}`), nil)
	if e != nil || !r.IsError || calls.Load() != 1 {
		t.Fatalf("replayed or nonterminal result: %+v %v %d", r, e, calls.Load())
	}
}

func TestCLIApprovalChangesAndOldToolCannotRun(t *testing.T) {
	dir := t.TempDir()
	exe, _ := os.Executable()
	manifestPath := filepath.Join(dir, "golder-plugin.toml")
	manifest := fmt.Sprintf("[plugin]\nid='demo'\n[[tools]]\nname='echo'\ncommand=[%q]\ninput_schema='{\"type\":\"object\"}'\n", exe)
	if e := os.WriteFile(manifestPath, []byte(manifest), 0o600); e != nil {
		t.Fatal(e)
	}
	ref := SourceRef{Kind: "plugin", ID: "demo"}
	m := testManager(t, ref, SourceConfig{Enabled: true, Runtime: "cli", Manifest: manifestPath}, nil)
	if len(m.Tools()) != 0 {
		t.Fatal("unapproved CLI advertised")
	}
	if e := m.Approve(ref); e != nil {
		t.Fatal(e)
	}
	if len(m.Tools()) != 1 {
		t.Fatal(m.List())
	}
	old := m.Tools()[0]
	if e := os.WriteFile(manifestPath, []byte(manifest+"# changed\n"), 0o600); e != nil {
		t.Fatal(e)
	}
	if e := m.Approve(ref); e == nil {
		t.Fatal("approved a changed review")
	}
	r, e := old.Execute(agentcore.WithExternalToolAuthorization(context.Background()), "old", json.RawMessage(`{}`), nil)
	if e != nil || !r.IsError {
		t.Fatal("changed source executed")
	}
}
