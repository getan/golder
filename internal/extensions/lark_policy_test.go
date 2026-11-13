package extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getan/golder/extension"
	"github.com/getan/golder/internal/agentcore"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestLarkPolicyCoversAllConfiguredOperations(t *testing.T) {
	lark := SourceRef{Kind: "mcp", ID: "lark"}
	for _, name := range []string{"doc_write", "drive_write", "message_send", "ticket_update", "ticket_transition", "sheet_write", "sheet_structure", "base_record_write", "calendar_write", "task_write", "wiki_write", "file_transfer", "cli_call", "cli_raw"} {
		t.Run(name, func(t *testing.T) {
			tool := &tool{ref: lark, spec: extension.ToolSpec{Name: name}}
			operator, policy := tool.approvalFor(&source{cfg: SourceConfig{}})
			if operator || !policy {
				t.Fatalf("known Lark/Meegle write: operator=%t policy=%t", operator, policy)
			}
		})
	}
	alias := &tool{ref: SourceRef{Kind: "mcp", ID: "meegle"}, spec: extension.ToolSpec{Name: "ticket_update"}}
	operator, policy := alias.approvalFor(&source{cfg: SourceConfig{Transport: "stdio", Command: filepath.Join("bin", executableName("golder-lark"))}})
	if operator || !policy {
		t.Fatalf("aliased native Lark/Meegle source: operator=%t policy=%t", operator, policy)
	}
	for _, tc := range []struct {
		name             string
		ref              SourceRef
		tool             string
		patterns         []string
		operator, policy bool
	}{
		{name: "future configured Lark tool", ref: lark, tool: "new_operation", patterns: []string{"*"}, policy: true},
		{name: "unconfigured Lark read", ref: lark, tool: "doc_read"},
		{name: "other MCP keeps human confirmation", ref: SourceRef{Kind: "mcp", ID: "other"}, tool: "doc_write", patterns: []string{"*"}, operator: true},
		{name: "other plugin keeps human confirmation", ref: SourceRef{Kind: "plugin", ID: "lark"}, tool: "doc_write", patterns: []string{"*"}, operator: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tool := &tool{ref: tc.ref, spec: extension.ToolSpec{Name: tc.tool}}
			operator, policy := tool.approvalFor(&source{cfg: SourceConfig{ConfirmTools: tc.patterns}})
			if operator != tc.operator || policy != tc.policy {
				t.Fatalf("operator=%t policy=%t, want operator=%t policy=%t", operator, policy, tc.operator, tc.policy)
			}
		})
	}
}

func TestMCPBackendSeparatesPolicyAndHumanMetadata(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	received := make(chan mcp.Meta, 1)
	server.AddTool(&mcp.Tool{Name: "doc_write", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		received <- req.Params.Meta
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	})
	clientSide, serverSide := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ss, err := server.Connect(ctx, serverSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	backend := &mcpBackend{session: cs, progress: map[string]func(extension.Progress){}}
	for i, tc := range []struct{ human, policy bool }{{false, false}, {false, true}, {true, false}, {true, true}} {
		call := extension.Call{ID: fmt.Sprint(i), Tool: "doc_write", Arguments: json.RawMessage(`{"policy_approved":true}`), OperatorConfirmed: tc.human, PolicyApproved: tc.policy}
		result, err := backend.Call(ctx, call, nil)
		if err != nil || result.IsError {
			t.Fatalf("call=%+v result=%+v err=%v", tc, result, err)
		}
		meta := <-received
		if (meta[extension.OperatorConfirmedMeta] == true) != tc.human || (meta[extension.PolicyApprovedMeta] == true) != tc.policy {
			t.Fatalf("call=%+v metadata=%v", tc, meta)
		}
	}
}

func TestLarkToolRequiresExactPolicyAndForwardsIt(t *testing.T) {
	var calls atomic.Int64
	received := make(chan mcp.Meta, 1)
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{Name: "doc_write", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		received <- req.Params.Meta
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true})
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	ref := SourceRef{Kind: "mcp", ID: "lark"}
	m := testManager(t, ref, SourceConfig{Enabled: true, Transport: "http", URL: httpServer.URL}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := m.Approve(ref); err != nil {
		t.Fatal(err)
	}
	if err := m.Connect(ctx, ref); err != nil {
		t.Fatal(err)
	}
	tools := m.Tools()
	if len(tools) != 1 {
		t.Fatalf("unexpected tools: %+v", m.List())
	}
	tool := tools[0]
	policy, ok := tool.(interface{ RequiresPolicyApproval() bool })
	if !ok || !policy.RequiresPolicyApproval() {
		t.Fatal("Lark write did not request host policy approval")
	}
	base := agentcore.WithNetworkGrant(agentcore.WithExternalToolAuthorization(ctx))
	args := json.RawMessage(`{"confirm":true}`)
	result, err := tool.Execute(base, "one", args, nil)
	if err != nil || !result.IsError || calls.Load() != 0 {
		t.Fatalf("unapproved call ran: result=%+v err=%v calls=%d", result, err, calls.Load())
	}
	wrong := agentcore.WithPolicyApproval(base, "other", tool.Name(), args)
	result, err = tool.Execute(wrong, "one", args, nil)
	if err != nil || !result.IsError || calls.Load() != 0 {
		t.Fatalf("approval for another call was accepted: result=%+v err=%v calls=%d", result, err, calls.Load())
	}
	approved := agentcore.WithPolicyApproval(base, "one", tool.Name(), args)
	result, err = tool.Execute(approved, "one", args, nil)
	if err != nil || result.IsError || calls.Load() != 1 {
		t.Fatalf("policy-approved call failed: result=%+v err=%v calls=%d", result, err, calls.Load())
	}
	meta := <-received
	if meta[extension.PolicyApprovedMeta] != true || meta[extension.OperatorConfirmedMeta] == true {
		t.Fatalf("automatic approval was not forwarded separately from human approval: %v", meta)
	}
	original, err := m.get(ref)
	if err != nil {
		t.Fatal(err)
	}
	replacement := &source{ref: ref, cfg: original.cfg}
	replacement.enabled.Store(true)
	m.mu.Lock()
	m.sources[ref.String()] = replacement
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.sources[ref.String()] = original
		m.mu.Unlock()
	}()
	approved = agentcore.WithPolicyApproval(base, "two", tool.Name(), args)
	result, err = tool.Execute(approved, "two", args, nil)
	if err != nil || !result.IsError || calls.Load() != 1 || !strings.Contains(agentcore.ContentToText(result.Content), "source changed") {
		t.Fatalf("source replacement redirected an approved call: result=%+v err=%v calls=%d", result, err, calls.Load())
	}
}
