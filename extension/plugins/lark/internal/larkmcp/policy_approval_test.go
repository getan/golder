package larkmcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/extension"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPolicyMetadataAuthorizesLarkAndMeegleWrites(t *testing.T) {
	f := &fakeRunner{fn: func(service string, args []string, _ []byte) (Output, error) {
		if service == "project" && len(args) > 1 {
			switch args[1] {
			case "get":
				return Output{Stdout: []byte(`{"work_item_type":{"key":"bug"}}`)}, nil
			case "meta-fields":
				return Output{Stdout: []byte(`{"fields":[{"field_key":"priority","editable":true}]}`)}, nil
			}
		}
		return Output{Stdout: []byte(`{"ok":true,"identity":"user","data":{}}`)}, nil
	}}
	p := New(Config{}, f)
	clientSide, serverSide := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ss, err := p.Server("test").Connect(ctx, serverSide, nil)
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

	for _, tc := range []struct {
		name      string
		arguments map[string]any
		wantCalls int
		wantWrite string
	}{
		{
			name: "doc_write", arguments: map[string]any{"operation": "append", "options": map[string]any{"doc": "example-doc", "content": "test"}, "confirm": true},
			wantCalls: 3, wantWrite: "lark docs +update",
		},
		{
			name: "ticket_update", arguments: map[string]any{"options": map[string]any{"project_key": "project", "work_item_id": "123", "fields": []any{map[string]any{"field_key": "priority", "field_value": "P1"}}}, "confirm": true},
			wantCalls: 4, wantWrite: "project workitem update",
		},
		{
			name: "cli_call", arguments: map[string]any{"service": "lark", "argv": []string{"sheets", "+styles-put", "--url=https://tenant.feishu.cn/sheets/example", `--styles={"styles":[]}`}, "confirm": true},
			wantCalls: 1, wantWrite: "lark sheets +styles-put",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.calls = nil
			without, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: tc.name, Arguments: tc.arguments})
			if err != nil || !without.IsError || len(f.calls) != 0 {
				t.Fatalf("model intent bypassed host policy: result=%+v err=%v calls=%v", without, err, f.calls)
			}
			wrongType, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: tc.name, Arguments: tc.arguments, Meta: mcp.Meta{extension.PolicyApprovedMeta: "true"}})
			if err != nil || !wrongType.IsError || len(f.calls) != 0 {
				t.Fatalf("non-boolean metadata bypassed host policy: result=%+v err=%v calls=%v", wrongType, err, f.calls)
			}
			allowed, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: tc.name, Arguments: tc.arguments, Meta: mcp.Meta{extension.PolicyApprovedMeta: true}})
			if err != nil || allowed.IsError || len(f.calls) != tc.wantCalls {
				t.Fatalf("policy-approved operation failed: result=%+v err=%v calls=%v", allowed, err, f.calls)
			}
			writes := 0
			for _, call := range f.calls {
				if strings.Contains(call, tc.wantWrite) {
					writes++
				}
			}
			if writes != 1 {
				t.Fatalf("wanted one %q invocation, got %v", tc.wantWrite, f.calls)
			}
		})
	}
}
