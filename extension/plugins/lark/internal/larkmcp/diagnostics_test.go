package larkmcp

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestDocSearchReportsStructuredStderrAndRequiredScope(t *testing.T) {
	f := &fakeRunner{fn: func(service string, args []string, _ []byte) (Output, error) {
		want := []string{"drive", "+search", "--mine=true", "--page-size=20", "--as=user"}
		if service != "lark" || !reflect.DeepEqual(args, want) {
			t.Fatalf("literal filtered-search argv changed: %q", args)
		}
		return Output{Code: 1, Stderr: []byte(`{"ok":false,"error":{"type":"permission","code":99991672,"message":"Missing search:docs:read","missing_scopes":["search:docs:read"],"log_id":"request-123","http_status":403,"access_token":"private-access"}}`)}, nil
	}}
	r := New(Config{}, f).Call(context.Background(), "doc_search", json.RawMessage(`{"options":{"mine":true,"page_size":20}}`))
	d := r.Diagnostics
	if r.OK || r.Error.Code != "99991672" || !strings.Contains(r.Error.Message, "search:docs:read") || r.ExecutionState != "failed" || d == nil {
		t.Fatalf("public CLI error metadata was lost: %+v", r)
	}
	if d.ErrorSource != "stderr_json" || d.ExitCode == nil || *d.ExitCode != 1 || d.LogID != "request-123" || d.HTTPStatus != 403 || d.CLIType != "permission" || !reflect.DeepEqual(d.RequiredScopes, []string{"search:docs:read"}) || !reflect.DeepEqual(d.MissingScopes, d.RequiredScopes) {
		t.Fatalf("diagnostics incomplete: %+v", d)
	}
	encoded, _ := json.Marshal(r)
	if strings.Contains(string(encoded), "private-access") || len(f.calls) != 1 || !strings.Contains(r.Error.NextAction, "/mcp setup lark") {
		t.Fatal("diagnostics leaked a credential or retried/re-authorized a failed request")
	}
}

func TestDocSearchCreatorFilterAndEmptyArgumentsRemainDistinct(t *testing.T) {
	f := &fakeRunner{fn: func(_ string, args []string, _ []byte) (Output, error) {
		want := []string{"drive", "+search", "--created-by-me=true", "--page-size=20", "--query=", "--as=user"}
		if !reflect.DeepEqual(args, want) {
			t.Fatalf("creator filter or empty query was changed: %q", args)
		}
		return Output{Stdout: []byte(`{"ok":true,"identity":"user","data":{"results":[]}}`)}, nil
	}}
	p := New(Config{}, f)
	r := p.Call(context.Background(), "doc_search", json.RawMessage(`{"options":{"created_by_me":true,"page_size":20,"query":""}}`))
	if !r.OK || r.Diagnostics != nil {
		t.Fatalf("valid creator-only browsing was rejected: %+v", r)
	}
	args := []string{"drive", "+search", "--query", "", "--created-by-me", "--page-size", "20", "--as=user"}
	want := []string{"drive", "+search", "--query=<empty>", "--created-by-me", "--page-size=<redacted>", "--as=user"}
	if got := diagnosticArgv(args); !reflect.DeepEqual(got, want) {
		t.Fatalf("diagnostic argv obscures empty arguments: %q", got)
	}
	f.fn = func(_ string, got []string, _ []byte) (Output, error) {
		if !reflect.DeepEqual(got, args) {
			t.Fatalf("cli_call dropped the empty string argument: %q", got)
		}
		return Output{Code: 2, Stderr: []byte(`{"error":{"type":"validation","message":"example validation error"}}`)}, nil
	}
	raw, _ := json.Marshal(map[string]any{"service": "lark", "argv": args[:len(args)-1], "confirm": true})
	r = p.Call(approved(), "cli_call", raw)
	if r.OK || r.ExecutionState != "failed" || r.Diagnostics.CLIType != "validation" || !reflect.DeepEqual(r.Diagnostics.ArgvShape, want) {
		t.Fatalf("aggregate execution was replayed, reclassified, or lost argv boundaries: %+v", r)
	}
}

func TestCLIDiagnosticsPrivacyAcrossMessagesDataAndArguments(t *testing.T) {
	message := `app-secret-literal access_token=private-access refresh_token="private-refresh" Authorization: Bearer private-bearer device_code=private-device https://auth.example/device?token=private-url ` +
		"-----BEGIN PRIVATE KEY-----\nprivate-key-body\n-----END PRIVATE KEY-----"
	p := New(Config{AppSecret: "app-secret-literal"}, nil)
	for _, stream := range []string{"stdout", "stderr"} {
		payload, _ := json.Marshal(map[string]any{"ok": false, "error": map[string]any{"code": "ACCESS_DENIED", "message": message, "private_key": "private-key-value", "authorization": "private-header"}})
		out := Output{Code: 1}
		if stream == "stdout" {
			out.Stdout = payload
		} else {
			out.Stderr = payload
		}
		r := p.withCLIDiagnostics(normalize("lark", out, nil, false), []string{"docs", "+update", "--content=private-document", "--json", "{\"secret\":\"private-payload\"}"}, out, nil)
		encoded, _ := json.Marshal(r)
		for _, secret := range []string{"app-secret-literal", "private-access", "private-refresh", "private-bearer", "private-device", "private-url", "private-key-body", "private-key-value", "private-header", "private-document", "private-payload"} {
			if strings.Contains(string(encoded), secret) {
				t.Fatalf("%s diagnostics leaked %q: %s", stream, secret, encoded)
			}
		}
	}
}

func TestCLIDiagnosticsHandleNoticesButWithholdUnknownStderr(t *testing.T) {
	p := New(Config{}, nil)
	out := Output{Code: 4, Stdout: []byte(`{"code":0,"msg":"success"}`), Stderr: []byte("notice: private-notice\n{\n  \"error\": {\"type\":\"network\",\"code\":\"NETWORK_ERROR\",\"message\":\"connection timed out\"}\n}\n")}
	r := p.withCLIDiagnostics(normalize("lark", out, nil, false), []string{"drive", "+search"}, out, nil)
	if r.Error.Code != "NETWORK_ERROR" || r.Diagnostics.ErrorSource != "stderr_json" || r.Diagnostics.CLIType != "network" {
		t.Fatalf("notice or successful stdout hid the stderr error: %+v", r)
	}
	out = Output{Code: 4, Stderr: []byte("private free-form debug log Bearer private-token")}
	r = p.withCLIDiagnostics(normalize("lark", out, nil, false), []string{"drive", "+search"}, out, nil)
	encoded, _ := json.Marshal(r)
	if strings.Contains(string(encoded), "private-token") || strings.Contains(string(encoded), "private free-form") || r.Diagnostics.ErrorSource != "unstructured_stderr_withheld" || *r.Diagnostics.ExitCode != 4 {
		t.Fatalf("unknown stderr escaped the allowlist: %s", encoded)
	}
}

func TestCLIDiagnosticsKeepIdentityConfirmationAndTruncationRefusals(t *testing.T) {
	p := New(Config{}, nil)
	for _, tc := range []struct {
		out  Output
		code string
	}{
		{Output{Stdout: []byte(`{"ok":true,"identity":"bot"}`), Stderr: []byte(`{"error":{"code":"OTHER"}}`)}, "IDENTITY_MISMATCH"},
		{Output{Code: 10, Stderr: []byte(`{"error":{"code":"OTHER"}}`)}, "CONFIRMATION_REQUIRED"},
		{Output{Code: 1, Truncated: true, Stderr: []byte(`{"error":{"code":"OTHER","message":"private"}}`)}, "OUTPUT_LIMIT"},
	} {
		r := p.withCLIDiagnostics(normalize("lark", tc.out, nil, true), []string{"drive", "+search"}, tc.out, nil)
		if r.OK || r.Error.Code != tc.code {
			t.Fatalf("diagnostics weakened a refusal: %+v", r)
		}
	}
}
