package larkmcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCLIArgumentDiagnosticDoesNotEchoRawValues(t *testing.T) {
	out := Output{Code: 2, Stderr: []byte("Error: unknown flag: --example\nBearer private-token")}
	p := New(Config{}, nil)
	r := p.withCLIDiagnostics(normalize("lark", out, nil, false), []string{"drive", "+search", "--example=private-value"}, out, nil)
	b, _ := json.Marshal(r)
	if r.Error.Code != "CLI_ARGUMENT_ERROR" || !strings.Contains(r.Error.Message, "--example") || r.Diagnostics.CLIType != "validation" || strings.Contains(string(b), "private-token") || strings.Contains(string(b), "private-value") {
		t.Fatalf("unsafe or opaque argument diagnostic: %s", b)
	}
}

func TestCLIDiagnosticsDoNotReclassifyInterruptedOrUnreviewedCalls(t *testing.T) {
	f := &fakeRunner{fn: func(_ string, _ []string, _ []byte) (Output, error) {
		return Output{Stderr: []byte(`{"error":{"type":"permission","code":"OTHER"}}`)}, context.DeadlineExceeded
	}}
	p := New(Config{}, f)
	raw := json.RawMessage(`{"service":"lark","argv":["drive","+search","--query="],"confirm":true}`)
	r := p.Call(context.Background(), "cli_call", raw)
	if r.Error.Code != "OPERATOR_CONFIRMATION_REQUIRED" || len(f.calls) != 0 {
		t.Fatal("read-only classification bypassed exact-call authorization")
	}
	r = p.Call(approved(), "cli_call", raw)
	if r.Error.Code != "INTERRUPTED" || r.ExecutionState != "failed" || r.Diagnostics.ExitCode != nil || len(f.calls) != 1 {
		t.Fatalf("interrupted read was reclassified or replayed: %+v", r)
	}
	r = p.Call(approved(), "cli_call", json.RawMessage(`{"service":"lark","argv":["docs","+create","--title=example"],"confirm":true}`))
	if r.Error.Code != "INTERRUPTED" || r.ExecutionState != "unknown" || len(f.calls) != 2 {
		t.Fatalf("unknown write outcome was treated as a safe retry: %+v", r)
	}
}
