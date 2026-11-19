package larkmcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCLIHelpEmptyCommandDiscoversRootHelp(t *testing.T) {
	f := &fakeRunner{}
	p := New(Config{}, f)
	for _, service := range []string{"lark", "project"} {
		r := p.Call(context.Background(), "cli_help", json.RawMessage(`{"service":"`+service+`","command":[]}`))
		if !r.OK {
			t.Fatalf("%s root help failed: %+v", service, r)
		}
		if len(f.calls) == 0 || f.calls[len(f.calls)-1] != service+" --help" {
			t.Fatalf("%s root help argv = %v", service, f.calls)
		}
	}
}

func TestCLICallRawEntryMismatchExplainsTheFix(t *testing.T) {
	f := &fakeRunner{}
	p := New(Config{}, f)
	r := p.Call(approved(), "cli_call", json.RawMessage(`{"service":"lark","argv":["drive","files","list","--file-tokens=x"],"confirm":true}`))
	if r.OK || r.Error == nil || r.Error.Code != "COMMAND_BLOCKED" {
		t.Fatalf("raw command through cli_call was not blocked: %+v", r)
	}
	if !strings.Contains(r.Error.NextAction, "cli_raw") || !strings.Contains(r.Error.NextAction, "not executed") {
		t.Fatalf("entry mismatch did not name cli_raw: %q", r.Error.NextAction)
	}
	if len(f.calls) != 0 {
		t.Fatalf("blocked entry still ran: %v", f.calls)
	}
}

func TestCLIHelpInvalidPathPointsAtRootHelp(t *testing.T) {
	f := &fakeRunner{}
	p := New(Config{}, f)
	r := p.Call(context.Background(), "cli_help", json.RawMessage(`{"service":"lark","command":["auth","login"]}`))
	if r.OK || r.Error == nil || r.Error.Code != "COMMAND_BLOCKED" {
		t.Fatalf("auth help was not blocked: %+v", r)
	}
	if !strings.Contains(r.Error.NextAction, "empty command") || !strings.Contains(r.Error.NextAction, "no command was executed") {
		t.Fatalf("help error did not point at root discovery: %q", r.Error.NextAction)
	}
}
