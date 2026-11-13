package larkmcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIRawAllowsRawCommandsAndHighRiskConfirmation(t *testing.T) {
	f := &fakeRunner{}
	p := New(Config{}, f)
	if r := p.Call(approved(), "cli_raw", json.RawMessage(`{"service":"lark","argv":["calendar","events","search_event","--calendar-id=primary"],"confirm":true}`)); !r.OK {
		t.Fatalf("raw read failed: %+v error=%+v", r, r.Error)
	}
	if r := p.Call(approved(), "cli_raw", json.RawMessage(`{"service":"lark","argv":["wiki","spaces","delete","--space-id=space"],"confirm":true,"high_risk":true}`)); !r.OK {
		t.Fatalf("high-risk raw call failed: %+v error=%+v", r, r.Error)
	}
	if len(f.calls) != 2 ||
		!strings.Contains(f.calls[0], "calendar events search_event") ||
		!strings.Contains(f.calls[1], "wiki spaces delete --space-id=space --yes --as=user") {
		t.Fatalf("raw calls = %v", f.calls)
	}
	if r := p.Call(approved(), "cli_raw", json.RawMessage(`{"service":"lark","argv":["auth","login"],"confirm":true}`)); r.OK || r.Error == nil || r.Error.Code != "COMMAND_BLOCKED" {
		t.Fatalf("raw auth command was not blocked: %+v", r)
	}
	if r := p.Call(approved(), "cli_raw", json.RawMessage(`{"service":"lark","argv":["mail","+send","--body=@/etc/passwd"],"confirm":true}`)); r.OK || r.Error == nil || r.Error.Code != "ARGUMENT_BLOCKED" {
		t.Fatalf("raw local file reference was not blocked: %+v", r)
	}
	if r := p.Call(context.Background(), "cli_raw", json.RawMessage(`{"service":"lark","argv":["calendar","events","search_event"],"confirm":true}`)); r.OK || r.Error == nil || r.Error.Code != "OPERATOR_CONFIRMATION_REQUIRED" {
		t.Fatalf("unapproved raw call was not gated: %+v", r)
	}
}

func TestFileTransferResolvesOnlyInsideWorkspace(t *testing.T) {
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := os.WriteFile("input.txt", []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeRunner{}
	p := New(Config{}, f)
	upload := json.RawMessage(`{"operation":"upload","options":{"service":"lark","path":"input.txt","argv":["drive","+upload","--file","{file}"]},"confirm":true}`)
	if r := p.Call(approved(), "file_transfer", upload); !r.OK {
		t.Fatalf("upload failed: %+v", r)
	}
	absolute, _ := filepath.Abs("input.txt")
	if len(f.calls) != 1 || !strings.Contains(f.calls[0], "--file "+absolute) {
		t.Fatalf("upload command = %v", f.calls)
	}
	outside := json.RawMessage(`{"operation":"upload","options":{"service":"lark","path":"../escape.txt","argv":["drive","+upload","--file","{file}"]},"confirm":true}`)
	if r := p.Call(approved(), "file_transfer", outside); r.OK || r.Error == nil || r.Error.Code != "INVALID_ARGUMENTS" || len(f.calls) != 1 {
		t.Fatalf("outside path was not rejected: %+v calls=%v", r, f.calls)
	}
	download := json.RawMessage(`{"operation":"download","options":{"service":"lark","path":"out/result.txt","argv":["drive","+download","--output={file}"]},"confirm":true}`)
	if r := p.Call(approved(), "file_transfer", download); !r.OK {
		t.Fatalf("download failed: %+v", r)
	}
	output, _ := filepath.Abs("out/result.txt")
	if len(f.calls) != 2 || !strings.Contains(f.calls[1], "--output="+output) {
		t.Fatalf("download command = %v", f.calls)
	}
	local := json.RawMessage(`{"operation":"upload","options":{"service":"lark","path":"input.txt","argv":["drive","+upload","--file=@input.txt"]},"confirm":true}`)
	if r := p.Call(approved(), "file_transfer", local); r.OK || r.Error == nil || r.Error.Code != "ARGUMENT_BLOCKED" || len(f.calls) != 2 {
		t.Fatalf("local @file was not rejected: %+v error=%+v calls=%v", r, r.Error, f.calls)
	}
}
