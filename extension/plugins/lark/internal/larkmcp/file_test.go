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

func TestFileTransferAcceptsRemoteResourceKeys(t *testing.T) {
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	f := &fakeRunner{}
	p := New(Config{}, f)

	// --file-key names a Lark attachment, not a local path: it must pass through
	// unchanged while --output carries the {file} placeholder.
	download := json.RawMessage(`{"operation":"download","options":{"service":"lark","path":"papers/Qwen-VL.pdf","argv":["im","+messages-resources-download","--message-id","om_x100b7f625c10e074b4aa3ee9ac74b3c","--file-key","file_v3_0017o_4614a9c1-556f-427e-a136-0cae9397995g","--type","file","--output","{file}"]},"confirm":true}`)
	if r := p.Call(approved(), "file_transfer", download); !r.OK {
		t.Fatalf("download with --file-key failed: %+v error=%+v", r, r.Error)
	}
	output, _ := filepath.Abs("papers/Qwen-VL.pdf")
	if len(f.calls) != 1 ||
		!strings.Contains(f.calls[0], "--file-key file_v3_0017o_4614a9c1-556f-427e-a136-0cae9397995g") ||
		!strings.Contains(f.calls[0], "--output "+output) {
		t.Fatalf("download command = %v", f.calls)
	}

	// The --flag=value form must be classified the same way.
	equals := json.RawMessage(`{"operation":"download","options":{"service":"lark","path":"out/report.pdf","argv":["im","+messages-resources-download","--message-id=om_x100b7f625c10e074b4aa3ee9ac74b3c","--file-key=file_v3_0017o_4614a9c1-556f-427e-a136-0cae9397995g","--output={file}"]},"confirm":true}`)
	if r := p.Call(approved(), "file_transfer", equals); !r.OK {
		t.Fatalf("download with --file-key=value failed: %+v error=%+v", r, r.Error)
	}
	second, _ := filepath.Abs("out/report.pdf")
	if len(f.calls) != 2 ||
		!strings.Contains(f.calls[1], "--file-key=file_v3_0017o_4614a9c1-556f-427e-a136-0cae9397995g") ||
		!strings.Contains(f.calls[1], "--output="+second) {
		t.Fatalf("equals-form command = %v", f.calls)
	}
}

func TestFileTransferKeepsLocalPathBoundaries(t *testing.T) {
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := os.WriteFile("input.txt", []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, "escape"); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	f := &fakeRunner{}
	p := New(Config{}, f)

	cases := []struct{ name, body string }{
		{"local --file path", `{"operation":"upload","options":{"service":"lark","path":"input.txt","argv":["drive","+upload","--file","/etc/passwd"]},"confirm":true}`},
		{"local --key-file path", `{"operation":"upload","options":{"service":"lark","path":"input.txt","argv":["drive","+upload","--key-file","/etc/passwd"]},"confirm":true}`},
		{"local --output path", `{"operation":"download","options":{"service":"lark","path":"out/result.txt","argv":["drive","+download","--output","/tmp/result.txt"]},"confirm":true}`},
		{"@file reference", `{"operation":"upload","options":{"service":"lark","path":"input.txt","argv":["drive","+upload","--file=@/etc/passwd"]},"confirm":true}`},
		{"stdin reference", `{"operation":"download","options":{"service":"lark","path":"out/result.txt","argv":["drive","+download","--output","-"]},"confirm":true}`},
		{"workspace escape", `{"operation":"download","options":{"service":"lark","path":"../escape.txt","argv":["drive","+download","--output","{file}"]},"confirm":true}`},
		{"symlink escape", `{"operation":"download","options":{"service":"lark","path":"escape/result.txt","argv":["drive","+download","--output","{file}"]},"confirm":true}`},
		{"missing placeholder", `{"operation":"download","options":{"service":"lark","path":"out/result.txt","argv":["drive","+download","--output","out/result.txt"]},"confirm":true}`},
		{"duplicate placeholders", `{"operation":"download","options":{"service":"lark","path":"out/result.txt","argv":["im","+messages-resources-download","--file-key","{file}","--output","{file}"]},"confirm":true}`},
		{"remote key as local path", `{"operation":"download","options":{"service":"lark","path":"out/result.txt","argv":["im","+messages-resources-download","--file-key","/etc/passwd","--output","{file}"]},"confirm":true}`},
	}
	for _, tc := range cases {
		before := len(f.calls)
		r := p.Call(approved(), "file_transfer", json.RawMessage(tc.body))
		if r.OK || r.Error == nil || (r.Error.Code != "ARGUMENT_BLOCKED" && r.Error.Code != "INVALID_ARGUMENTS") {
			t.Fatalf("%s was not rejected: %+v", tc.name, r)
		}
		if len(f.calls) != before {
			t.Fatalf("%s reached the runner: %v", tc.name, f.calls[before:])
		}
	}
}

func TestFlagClassificationSeparatesRemoteKeysFromLocalPaths(t *testing.T) {
	for _, key := range []string{"file-key", "file-keys", "image-key", "file-token", "cover-image-key", "message-id"} {
		if !remoteResourceFlag(key) || localPathFlag(key) {
			t.Fatalf("%s should be a remote identifier", key)
		}
	}
	for _, key := range []string{"file", "output", "output-dir", "key-file", "file-path", "attachment", "media"} {
		if remoteResourceFlag(key) || !localPathFlag(key) {
			t.Fatalf("%s should be a local path parameter", key)
		}
	}
}

func TestCLIRawAllowsRemoteResourceKeysButNotLocalFiles(t *testing.T) {
	f := &fakeRunner{}
	p := New(Config{}, f)
	raw := json.RawMessage(`{"service":"lark","argv":["im","+messages-resources-download","--message-id","om_x100b7f625c10e074b4aa3ee9ac74b3c","--file-key","file_v3_0017o_4614a9c1-556f-427e-a136-0cae9397995g"],"confirm":true}`)
	if r := p.Call(approved(), "cli_raw", raw); !r.OK {
		t.Fatalf("raw call with --file-key failed: %+v error=%+v", r, r.Error)
	}
	if len(f.calls) != 1 || !strings.Contains(f.calls[0], "--file-key file_v3_0017o_4614a9c1-556f-427e-a136-0cae9397995g") {
		t.Fatalf("raw call = %v", f.calls)
	}
	local := json.RawMessage(`{"service":"lark","argv":["drive","+upload","--file","/etc/passwd"],"confirm":true}`)
	if r := p.Call(approved(), "cli_raw", local); r.OK || r.Error == nil || r.Error.Code != "ARGUMENT_BLOCKED" || len(f.calls) != 1 {
		t.Fatalf("raw local file path was not blocked: %+v calls=%v", r, f.calls)
	}
}
