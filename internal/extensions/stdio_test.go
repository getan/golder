package extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/extension"
	pluginsdk "github.com/getan/golder/extension/plugin"
	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/seatbelt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestExtensionChild(t *testing.T) {
	mode := os.Getenv("EXTENSION_CHILD_MODE")
	if mode == "" {
		return
	}
	var err error
	switch mode {
	case "cli":
		fmt.Print("cli-result")
		os.Exit(0)
	case "rpc":
		err = pluginsdk.Serve(context.Background(), pluginsdk.Spec{ID: "fixture", Tools: []pluginsdk.Tool{{Spec: extension.ToolSpec{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handle: func(context.Context, extension.Call, func(extension.Progress)) (extension.Result, error) {
			return extension.Text("rpc-result"), nil
		}}}})
	case "mcp":
		s := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
		s.AddTool(&mcp.Tool{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "mcp-result"}}}, nil
		})
		err = s.Run(context.Background(), &mcp.StdioTransport{})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func TestManagedProcessAdapters(t *testing.T) {
	if !seatbelt.Available() {
		t.Skip("OS sandbox unavailable")
	}
	exe, _ := os.Executable()
	for _, mode := range []string{"cli", "rpc", "mcp"} {
		t.Run(mode, func(t *testing.T) {
			c := SourceConfig{Enabled: true, Runtime: mode, Command: exe, Args: []string{"-test.run=^TestExtensionChild$"}, Env: map[string]string{"EXTENSION_CHILD_MODE": mode}, CallTimeout: "5s"}
			ref := SourceRef{Kind: "plugin", ID: mode}
			if mode == "mcp" {
				ref.Kind = "mcp"
				c.Runtime = ""
				c.Transport = "stdio"
			}
			if mode == "cli" {
				path := filepath.Join(t.TempDir(), "golder-plugin.toml")
				text := fmt.Sprintf("[plugin]\nid='cli'\n[[tools]]\nname='echo'\ncommand=[%q,'-test.run=^TestExtensionChild$']\ninput_schema='{\"type\":\"object\"}'\n", exe)
				if e := os.WriteFile(path, []byte(text), 0o600); e != nil {
					t.Fatal(e)
				}
				c.Command = ""
				c.Args = nil
				c.Manifest = path
			}
			m := testManager(t, ref, c, nil)
			if e := m.Approve(ref); e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if e := m.Connect(ctx, ref); e != nil {
				logs, _ := m.Logs(ref)
				t.Fatalf("connect %s: %v\n%s", mode, e, logs)
			}
			tools := m.Tools()
			if len(tools) != 1 {
				t.Fatalf("directory: %+v", m.List())
			}
			r, e := tools[0].Execute(agentcore.WithExternalToolAuthorization(ctx), "call", json.RawMessage(`{}`), nil)
			text := agentcore.ContentToText(r.Content)
			if e != nil || r.IsError || !strings.Contains(text, mode+"-result") {
				logs, _ := m.Logs(ref)
				t.Fatalf("call %s: %+v %v\n%s", mode, r, e, logs)
			}
		})
	}
}
