package larkmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/extension"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeRunner struct {
	calls []string
	fn    func(string, []string, []byte) (Output, error)
}

func (f *fakeRunner) Run(_ context.Context, service string, args []string, input []byte) (Output, error) {
	f.calls = append(f.calls, service+" "+strings.Join(args, " "))
	if f.fn != nil {
		return f.fn(service, args, input)
	}
	return Output{Stdout: []byte(`{"ok":true,"identity":"user","data":{"items":[]}}`)}, nil
}

func approved() context.Context {
	return context.WithValue(context.Background(), operatorKey{}, true)
}

func TestCatalogInitializationIsOfflineAndStable(t *testing.T) {
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
	tools, err := cs.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 22 || len(f.calls) != 0 {
		t.Fatalf("catalog=%v, calls=%v, err=%v", tools, f.calls, err)
	}
	for _, d := range definitions() {
		if !bytes.Equal(d.schema(), d.schema()) {
			t.Fatalf("unstable schema: %s", d.Name)
		}
	}
	args := map[string]any{"confirm": true, "options": map[string]any{"chat_id": "oc_test", "text": "hello"}}
	r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "message_send", Arguments: args})
	if err != nil || !r.IsError || len(f.calls) != 0 {
		t.Fatalf("model boolean bypassed confirmation: %+v %v", r, err)
	}
	r, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "message_send", Arguments: args, Meta: mcp.Meta{extension.OperatorConfirmedMeta: true}})
	if err != nil || r.IsError || len(f.calls) != 1 || !strings.Contains(f.calls[0], "--as=user") {
		t.Fatalf("approved MCP call: %+v %v %v", r, err, f.calls)
	}
}

func TestWriteIntentAndCLIArguments(t *testing.T) {
	f := &fakeRunner{}
	p := New(Config{}, f)
	r := p.Call(context.Background(), "message_send", json.RawMessage(`{"options":{"chat_id":"oc_x","text":"hello"},"confirm":true}`))
	if r.OK || len(f.calls) != 0 {
		t.Fatal("model intent was treated as authority")
	}
	r = p.Call(approved(), "message_send", json.RawMessage(`{"options":{"chat_id":"oc_x","text":"hello; $(do-not-execute)"},"confirm":true}`))
	if !r.OK || r.ExecutionState != "submitted" || len(f.calls) != 1 || !strings.Contains(f.calls[0], "--text=hello; $(do-not-execute)") {
		t.Fatalf("literal argv: %+v %v", r, f.calls)
	}
	r = p.Call(approved(), "message_send", json.RawMessage(`{"options":{"chat_id":"oc_x","text":"@/private/secret"},"confirm":true}`))
	if r.OK || len(f.calls) != 1 {
		t.Fatal("file reference reached the runner")
	}
}

func TestLongTailPolicy(t *testing.T) {
	for _, args := range [][]string{
		{"auth", "login"}, {"config", "init"}, {"sheets", "raw"}, {"base", "+record-delete"}, {"drive", "+permission-set"}, {"docs", "+script"},
	} {
		if err := businessCommand("lark", args, false); err == nil {
			t.Fatalf("allowed forbidden command %v", args)
		}
	}
	for _, args := range [][]string{{"--as=bot"}, {"--content=@/tmp/secret"}, {"--output-path=/tmp/out"}, {"--headers=x"}, {"--url=https://evil.example/a"}, {"--file=/tmp/a"}, {"--", "bash"}, {"--yes"}} {
		if err := businessFlags(args); err == nil {
			t.Fatalf("allowed forbidden flags %v", args)
		}
	}
	f := &fakeRunner{}
	p := New(Config{}, f)
	if r := p.Call(context.Background(), "cli_help", json.RawMessage(`{"service":"lark","command":["sheets","+styles-put"]}`)); !r.OK {
		t.Fatal(r)
	}
	if r := p.Call(approved(), "cli_call", json.RawMessage(`{"service":"lark","argv":["sheets","+styles-put","--url=https://tenant.feishu.cn/sheets/x","--styles={\"styles\":[]} mirrors"],"confirm":true}`)); !r.OK {
		t.Fatal(r)
	}
	if len(f.calls) != 2 || !strings.HasSuffix(f.calls[0], "--help") || !strings.HasSuffix(f.calls[1], "--as=user") {
		t.Fatal(f.calls)
	}
}

func TestProjectSearchEscapingAndPagination(t *testing.T) {
	params, err := searchParams(map[string]any{"project_key": "real", "work_item_type": "bug", "query": "a_' OR 1=1--%"})
	if err != nil || !strings.Contains(params["mql"].(string), "a\\_'' OR 1=1--\\%") || strings.Contains(params["mql"].(string), "SELECT *") {
		t.Fatalf("params=%v err=%v", params, err)
	}
	if _, err := searchParams(map[string]any{"project_key": "x`; DELETE", "work_item_type": "bug"}); err == nil {
		t.Fatal("identifier injection accepted")
	}
	params, err = searchParams(map[string]any{"project_key": "real", "session_id": "cursor", "group_pagination_list": []any{map[string]any{"group_id": "1", "page_num": 2}}})
	if err != nil || params["mql"] != nil {
		t.Fatal(params, err)
	}
}

func TestProjectUpdateDiscoversTypeAndUsesStringProtocol(t *testing.T) {
	f := &fakeRunner{fn: func(_ string, args []string, _ []byte) (Output, error) {
		if args[1] == "get" {
			return Output{Stdout: []byte(`{"work_item_type":{"key":"custom-type"},"workitem_fields":[]}`)}, nil
		}
		if args[1] == "meta-fields" {
			return Output{Stdout: []byte(`{"fields":[{"field_key":"owners","type":"multi-user"}]}`)}, nil
		}
		if args[1] == "update" {
			var payload map[string]any
			for _, a := range args {
				if strings.HasPrefix(a, "--params=") {
					if err := json.Unmarshal([]byte(strings.TrimPrefix(a, "--params=")), &payload); err != nil {
						t.Fatal(err)
					}
				}
			}
			value := payload["fields"].([]any)[0].(map[string]any)["field_value"]
			if value != `["project-user-key"]` {
				t.Fatalf("not string protocol: %v", value)
			}
		}
		return Output{Stdout: []byte(`{}`)}, nil
	}}
	r := New(Config{}, f).Call(approved(), "ticket_update", json.RawMessage(`{"options":{"project_key":"p","work_item_id":"123","fields":[{"field_key":"owners","field_value":["project-user-key"]}]},"confirm":true}`))
	if !r.OK || r.Verification != "readback_only" || len(f.calls) != 4 || !strings.Contains(f.calls[1], "custom-type") {
		t.Fatalf("result=%+v calls=%v", r, f.calls)
	}
}

func TestSheetsAndBaseGuards(t *testing.T) {
	if err := validateTableWrite(map[string]any{"sheets": []any{map[string]any{"name": "A", "mode": "overwrite", "data": []any{}}}}); err == nil {
		t.Fatal("overwrite without explicit intent")
	}
	schema := map[string]any{"fields": []any{map[string]any{"name": "Formula", "type": "formula"}, map[string]any{"name": "Title", "type": "text"}}}
	if err := validateBaseWrite("create", map[string]any{"create_records": []any{map[string]any{"Formula": "bad"}}}, schema); err == nil {
		t.Fatal("formula field was writable")
	}
	if err := validateBaseWrite("update", map[string]any{"update_records": map[string]any{"rec1": map[string]any{"Title": "ok"}}}, schema); err != nil {
		t.Fatal(err)
	}
	f := &fakeRunner{}
	p := New(Config{}, f)
	r := p.Call(approved(), "sheet_write", json.RawMessage(`{"options":{"spreadsheet_token":"token","sheet_id":"sheet","range":"A1","cells":[[{"value":"001"}]]},"confirm":true}`))
	if !r.OK || len(f.calls) != 3 || !strings.Contains(f.calls[0], "+workbook-info") || !strings.Contains(f.calls[2], "+cells-get") {
		t.Fatalf("result=%+v calls=%v", r, f.calls)
	}
}

func TestErrorPrivacyAndPartialOutcomes(t *testing.T) {
	r := normalize("lark", Output{Stdout: []byte(`{"ok":true,"identity":"bot","data":{}}`)}, nil, false)
	if r.OK || r.Error.Code != "IDENTITY_MISMATCH" {
		t.Fatal(r)
	}
	r = normalize("project", Output{Code: 10, Stderr: []byte("Bearer should-never-appear")}, nil, true)
	b, _ := json.Marshal(r)
	if r.OK || r.Error.Code != "CONFIRMATION_REQUIRED" || bytes.Contains(b, []byte("should-never-appear")) {
		t.Fatal(string(b))
	}
	r = normalize("project", Output{}, context.DeadlineExceeded, true)
	if r.ExecutionState != "unknown" {
		t.Fatal(r)
	}
	value := sanitize(map[string]any{"access_token": "secret", "refresh_token": "secret", "token": "doc-resource-id", "page_token": "cursor", "appSecret": "secret"}).(map[string]any)
	if value["access_token"] != nil || value["appSecret"] != nil || value["token"] != "doc-resource-id" || value["page_token"] != "cursor" {
		t.Fatal(value)
	}
	if normalize("project", Output{Stdout: []byte(`{"isError":true}`)}, nil, false).OK {
		t.Fatal("in-band project error was treated as success")
	}
}

func TestNativeWrapperAndChildEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wrapper")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := nativeExecutable(path); err == nil {
		t.Fatal("wrapper accepted")
	}
	t.Setenv("LARK_MCP_APP_SECRET", "private-app-secret")
	t.Setenv("ANTHROPIC_API_KEY", "private-provider-key")
	t.Setenv("PATH", "/usr/bin:/bin")
	env := strings.Join(childEnv(Config{Home: "/private/home", Profile: "source-profile", ProjectHost: "project.feishu.cn"}), "\n")
	if strings.Contains(env, "private-app-secret") || strings.Contains(env, "private-provider-key") || strings.Contains(env, "PATH=/usr/bin") || !strings.Contains(env, "PATH=/private/home/.no-executables") {
		t.Fatal("credential or helper path escaped the environment scrub")
	}
}

func TestStatusSeparatesServicesAndNetworkFailure(t *testing.T) {
	f := &fakeRunner{fn: func(service string, _ []string, _ []byte) (Output, error) {
		if service == "project" {
			return Output{Code: 2, Stdout: []byte(`{"authenticated":false,"reason":"server unreachable"}`)}, nil
		}
		return Output{Stdout: []byte(`{"appId":"cli_existing","identity":"user","verified":true}`)}, nil
	}}
	r := New(Config{LarkCLI: "/lark", ProjectCLI: "/meegle"}, f).Call(context.Background(), "status", json.RawMessage(`{}`))
	services := r.Data.(map[string]any)["services"].(map[string]any)
	if !r.OK || services["lark"].(map[string]any)["authenticated"] != true || services["project"].(map[string]any)["authenticated"] != false {
		t.Fatal(r)
	}
	if !strings.Contains(services["project"].(map[string]any)["error"].(string), "Network/server") {
		t.Fatal(r)
	}
}

func TestUnavailableRunnerDoesNotRetry(t *testing.T) {
	f := &fakeRunner{fn: func(string, []string, []byte) (Output, error) { return Output{}, errors.New("not installed") }}
	r := New(Config{}, f).Call(approved(), "message_send", json.RawMessage(`{"options":{"chat_id":"oc_x","text":"once"},"confirm":true}`))
	if r.OK || len(f.calls) != 1 || !strings.Contains(r.Error.NextAction, "no installer") {
		t.Fatal(r, f.calls)
	}
}
