package larkmcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestTicketSearchMCPSchemaDescribesAssigneeConstraint(t *testing.T) {
	f := &fakeRunner{}
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
	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range list.Tools {
		if tool.Name != "ticket_search" {
			continue
		}
		if !strings.Contains(tool.Description, "current_status_operator") || !strings.Contains(tool.Description, "watcher") {
			t.Fatalf("incomplete tool description: %s", tool.Description)
		}
		data, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Properties struct {
				Options struct {
					Properties struct {
						Assignee struct {
							Type        string   `json:"type"`
							Enum        []string `json:"enum"`
							Description string   `json:"description"`
						} `json:"assignee"`
						SessionID struct {
							Description string `json:"description"`
						} `json:"session_id"`
					} `json:"properties"`
				} `json:"options"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(data, &schema); err != nil {
			t.Fatal(err)
		}
		assignee := schema.Properties.Options.Properties.Assignee
		if assignee.Type != "string" || len(assignee.Enum) != 1 || assignee.Enum[0] != "me" {
			t.Fatalf("unexpected assignee schema: %+v", assignee)
		}
		for _, want := range []string{"user_key", "open_id", "email", "current_status_operator", "watchers"} {
			if !strings.Contains(assignee.Description, want) {
				t.Fatalf("assignee description missing %q: %s", want, assignee.Description)
			}
		}
		if !strings.Contains(schema.Properties.Options.Properties.SessionID.Description, "not reapplied") {
			t.Fatal("continuation semantics not documented")
		}
		if len(f.calls) != 0 {
			t.Fatalf("catalog discovery executed a CLI: %v", f.calls)
		}
		args := map[string]any{"options": map[string]any{"project_key": "project", "work_item_type": "bug", "assignee": "1234567890123456789"}}
		invalid, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "ticket_search", Arguments: args})
		if err != nil || !invalid.IsError || len(invalid.Content) != 1 || len(f.calls) != 0 {
			t.Fatalf("invalid MCP call result=%+v err=%v calls=%v", invalid, err, f.calls)
		}
		text, ok := invalid.Content[0].(*mcp.TextContent)
		if !ok {
			t.Fatalf("unexpected error content: %T", invalid.Content[0])
		}
		var failure Result
		if err := json.Unmarshal([]byte(text.Text), &failure); err != nil || failure.Error == nil || failure.Error.Code != "INVALID_QUERY" || !strings.Contains(failure.Error.NextAction, "'me'") {
			t.Fatalf("invalid MCP error=%+v err=%v", failure, err)
		}
		args["options"].(map[string]any)["assignee"] = "me"
		valid, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "ticket_search", Arguments: args})
		if err != nil || valid.IsError || len(f.calls) != 1 {
			t.Fatalf("valid MCP call result=%+v err=%v calls=%v", valid, err, f.calls)
		}
		return
	}
	t.Fatal("ticket_search absent from MCP catalog")
}

func TestTicketSearchAssigneeInvalidDoesNotInvokeCLI(t *testing.T) {
	for _, value := range []string{`"1234567890123456789"`, `"other"`, `""`, `1234567890123456789`, `null`, `[]`, `{}`} {
		t.Run(value, func(t *testing.T) {
			f := &fakeRunner{}
			p := New(Config{}, f)
			for _, extra := range []string{`"work_item_type":"bug"`, `"session_id":"cursor","group_pagination_list":[]`} {
				raw := `{"options":{"project_key":"project",` + extra + `,"assignee":` + value + `}}`
				r := p.Call(context.Background(), "ticket_search", json.RawMessage(raw))
				if r.OK || r.Error == nil || r.Error.Code != "INVALID_QUERY" || !strings.Contains(r.Error.NextAction, "'me'") || strings.Contains(r.Error.NextAction, "long-tail") || len(f.calls) != 0 {
					t.Fatalf("result=%+v calls=%v", r, f.calls)
				}
			}
		})
	}
}

func TestTicketSearchMeUsesOnlyCurrentStatusOperator(t *testing.T) {
	f := &fakeRunner{}
	p := New(Config{}, f)
	result := p.Call(context.Background(), "ticket_search", json.RawMessage(`{"options":{"project_key":"project","work_item_type":"bug","assignee":"me"}}`))
	if !result.OK || len(f.calls) != 1 || !strings.Contains(f.calls[0], "array_contains(`current_status_operator`, current_login_user())") || strings.Contains(f.calls[0], " user me") {
		t.Fatalf("result=%+v calls=%v", result, f.calls)
	}
	f.calls = nil
	result = p.Call(context.Background(), "ticket_search", json.RawMessage(`{"options":{"project_key":"project","work_item_type":"bug"}}`))
	if !result.OK || len(f.calls) != 1 || strings.Contains(f.calls[0], "current_status_operator") {
		t.Fatalf("omitting assignee did not omit its filter: result=%+v calls=%v", result, f.calls)
	}
}

func TestProjectHelpUsesRealCommandFamily(t *testing.T) {
	f := &fakeRunner{}
	p := New(Config{}, f)
	blocked := p.Call(context.Background(), "cli_help", json.RawMessage(`{"service":"project","command":["ticket","search"]}`))
	if blocked.OK || blocked.Error == nil || blocked.Error.Code != "COMMAND_BLOCKED" || !strings.Contains(blocked.Error.NextAction, `"command":["workitem","query"]`) || len(f.calls) != 0 {
		t.Fatalf("blocked help result=%+v calls=%v", blocked, f.calls)
	}
	allowed := p.Call(context.Background(), "cli_help", json.RawMessage(`{"service":"project","command":["workitem","query"]}`))
	if !allowed.OK || len(f.calls) != 1 || f.calls[0] != "project workitem query --help" {
		t.Fatalf("allowed help result=%+v calls=%v", allowed, f.calls)
	}
	blocked = p.Call(context.Background(), "cli_help", json.RawMessage(`{"service":"project","command":["auth","login"]}`))
	if blocked.OK || blocked.Error == nil || blocked.Error.Code != "COMMAND_BLOCKED" || len(f.calls) != 1 {
		t.Fatalf("auth help policy changed: result=%+v calls=%v", blocked, f.calls)
	}
}
