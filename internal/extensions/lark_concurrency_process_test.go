//go:build unix

package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/extension"
	"github.com/getan/golder/internal/agentcore"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestLarkConcurrentMCPFixture(t *testing.T) {
	if os.Getenv("LARK_STATE_FIXTURE") != "1" {
		return
	}
	home := os.Getenv("HOME")
	s := mcp.NewServer(&mcp.Implementation{Name: "golder-lark", Version: "parallel-fixture"}, nil)
	for _, name := range []string{"doc_read", "doc_write"} {
		name := name
		s.AddTool(&mcp.Tool{Name: name, InputSchema: json.RawMessage(`{"type":"object","required":["options"],"properties":{"options":{"type":"object","required":["doc"],"properties":{"doc":{"type":"string"},"scope":{"type":"string"}}},"confirm":{"type":"boolean"}}}`)}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if name == "doc_write" && req.Params.Meta[extension.PolicyApprovedMeta] != true {
				return nil, errors.New("exact-call approval was not forwarded")
			}
			var in struct {
				Options struct {
					Doc   string `json:"doc"`
					Scope string `json:"scope"`
				} `json:"options"`
			}
			if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
				return nil, err
			}
			id := strings.TrimPrefix(larkResource(in.Options.Doc, "document"), "document:")
			if !larkResourceToken.MatchString(id) {
				return nil, errors.New("bad fixture id")
			}
			// Simulate the official CLI's short account-token transaction. This
			// lock ends before the (possibly slow) business request starts.
			unlock, err := lockStateContext(ctx, filepath.Join(home, "native-refresh.lock"))
			if err != nil {
				return nil, err
			}
			previous, _ := os.ReadFile(filepath.Join(home, "native-refresh-state"))
			err = os.WriteFile(filepath.Join(home, "native-refresh-state"), append(previous, 'x'), 0o600)
			unlock()
			if err != nil {
				return nil, err
			}
			marker := filepath.Join(home, "active-"+id)
			if err := os.WriteFile(marker, []byte(name), 0o600); err != nil {
				return nil, err
			}
			defer os.Remove(marker)
			if in.Options.Scope == "hold" || in.Options.Scope == "drain" {
				ticker := time.NewTicker(10 * time.Millisecond)
				defer ticker.Stop()
				for {
					if _, err := os.Stat(filepath.Join(home, "release-"+id)); err == nil {
						break
					}
					select {
					case <-ctx.Done():
						if in.Options.Scope == "drain" {
							_ = os.WriteFile(filepath.Join(home, "draining-"+id), []byte("draining"), 0o600)
							deadline := time.NewTimer(12 * time.Second)
							defer deadline.Stop()
							for {
								if _, err := os.Stat(filepath.Join(home, "cleanup-"+id)); err == nil {
									return nil, ctx.Err()
								}
								select {
								case <-ticker.C:
								case <-deadline.C:
									return nil, errors.New("fixture cleanup deadline")
								}
							}
						}
						return nil, ctx.Err()
					case <-ticker.C:
					}
				}
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "concurrent-result-" + id}}}, nil
		})
	}
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func concurrentLarkManagers(t *testing.T, ctx context.Context) (*Manager, *Manager, SourceRef) {
	t.Helper()
	a, b, ref := larkStateManagersForFixture(t, "TestLarkConcurrentMCPFixture")
	for _, m := range []*Manager{a, b} {
		if err := m.Connect(ctx, ref); err != nil {
			t.Fatal(err)
		}
		s, _ := m.get(ref)
		s.conn.(*larkStateBackend).concurrentCLI = func() bool { return true } // native test double only
	}
	return a, b, ref
}

func larkFixtureTool(t *testing.T, m *Manager, name string) agentcore.AgentTool {
	t.Helper()
	for _, tool := range m.Tools() {
		if strings.HasSuffix(tool.Name(), "__"+name) {
			return tool
		}
	}
	t.Fatalf("fixture tool %s not found", name)
	return nil
}

func TestLarkManagersOverlapSameAndSeparateConversations(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	a, b, ref := concurrentLarkManagers(t, ctx)
	home := sourceHome(ref)
	done := make(chan agentcore.AgentToolResult, 3)
	for i, manager := range []*Manager{a, a, b} {
		tool := larkFixtureTool(t, manager, "doc_read")
		id := fmt.Sprintf("doc%d", i)
		args := json.RawMessage(fmt.Sprintf(`{"options":{"doc":%q,"scope":"hold"}}`, id))
		go func() {
			r, _ := tool.Execute(agentcore.WithExternalToolAuthorization(ctx), id, args, nil)
			done <- r
		}()
	}
	// A barrier, not a timing comparison: none can finish until ALL three
	// independently sandboxed helpers have reached their business operation.
	for i := range 3 {
		waitLarkStateFile(t, ctx, filepath.Join(home, fmt.Sprintf("active-doc%d", i)))
	}
	if err := b.Reset(ref); !errors.Is(err, errStateInUse) {
		t.Fatalf("reset raced active parallel helpers: %v", err)
	}
	if err := b.Setup(ctx, ref, nil); !errors.Is(err, errStateInUse) {
		t.Fatalf("authorization raced active parallel helpers: %v", err)
	}
	for i := range 3 {
		if err := os.WriteFile(filepath.Join(home, fmt.Sprintf("release-doc%d", i)), []byte("go"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for range 3 {
		if r := <-done; r.IsError {
			t.Fatalf("parallel business call failed: %s", agentcore.ContentToText(r.Content))
		}
	}
	if state, err := os.ReadFile(filepath.Join(home, "native-refresh-state")); err != nil || string(state) != "xxx" {
		t.Fatalf("short native refresh transactions raced or calls replayed: %q %v", state, err)
	}
}

func TestLarkParallelCancellationKeepsConflictLeaseNotWholeSource(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	a, b, ref := concurrentLarkManagers(t, ctx)
	home := sourceHome(ref)
	t.Cleanup(func() { _ = os.WriteFile(filepath.Join(home, "cleanup-first"), []byte("go"), 0o600) })
	write := larkFixtureTool(t, a, "doc_write")
	args := json.RawMessage(`{"options":{"doc":"first","scope":"drain"},"confirm":true}`)
	activeCtx, cancelActive := context.WithCancel(agentcore.WithPolicyApproval(agentcore.WithExternalToolAuthorization(ctx), "cancel-first", write.Name(), args))
	defer cancelActive()
	done := make(chan agentcore.AgentToolResult, 1)
	go func() { r, _ := write.Execute(activeCtx, "cancel-first", args, nil); done <- r }()
	waitLarkStateFile(t, ctx, filepath.Join(home, "active-first"))
	cancelActive()
	waitLarkStateFile(t, ctx, filepath.Join(home, "draining-first"))
	select {
	case result := <-done:
		if !result.IsError || !strings.Contains(agentcore.ContentToText(result.Content), "still draining") {
			t.Fatalf("cancelled caller lost cleanup/outcome warning: %s", agentcore.ContentToText(result.Content))
		}
	case <-ctx.Done():
		t.Fatal("cancel did not return within its bounded drain wait")
	}
	read := larkFixtureTool(t, b, "doc_read")
	r, _ := read.Execute(agentcore.WithExternalToolAuthorization(ctx), "unrelated", json.RawMessage(`{"options":{"doc":"second"}}`), nil)
	if r.IsError {
		t.Fatalf("unrelated document blocked by draining writer: %s", agentcore.ContentToText(r.Content))
	}
	conflictCtx, stop := context.WithTimeout(agentcore.WithExternalToolAuthorization(ctx), 150*time.Millisecond)
	r, _ = read.Execute(conflictCtx, "conflict", json.RawMessage(`{"options":{"doc":"https://tenant.feishu.cn/docx/first"}}`), nil)
	stop()
	if !r.IsError || !strings.Contains(agentcore.ContentToText(r.Content), "was not dispatched") {
		t.Fatalf("conflicting reader entered during writer cleanup: %s", agentcore.ContentToText(r.Content))
	}
	if err := b.Reset(ref); !errors.Is(err, errStateInUse) {
		t.Fatalf("draining writer lost maintenance exclusion: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "cleanup-first"), []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, _ = read.Execute(agentcore.WithExternalToolAuthorization(ctx), "after-cleanup", json.RawMessage(`{"options":{"doc":"first"}}`), nil)
	if r.IsError {
		t.Fatalf("resource did not become usable after confirmed exit: %s", agentcore.ContentToText(r.Content))
	}
}
