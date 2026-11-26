package larkmcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRawAPIGETAndWriteGating(t *testing.T) {
	f := &fakeRunner{}
	p := New(Config{}, f)
	get := json.RawMessage(`{"service":"lark","argv":["api","GET","/open-apis/calendar/v4/calendars"],"confirm":true}`)
	if r := p.Call(approved(), "cli_raw", get); !r.OK {
		t.Fatalf("api GET failed: %+v error=%+v", r, r.Error)
	}
	if len(f.calls) != 1 || f.calls[0] != "lark api GET /open-apis/calendar/v4/calendars --as=user" {
		t.Fatalf("api GET call = %v", f.calls)
	}

	body := `{"service":"lark","argv":["api","POST","/open-apis/drive/v1/export_tasks","--data","{\"file_extension\":\"docx\",\"token\":\"docx_token\",\"type\":\"docx\"}"],"confirm":true}`
	if r := p.Call(approved(), "cli_raw", json.RawMessage(body)); r.OK || r.Error == nil || r.Error.Code != "HIGH_RISK_REQUIRED" {
		t.Fatalf("api POST without high_risk was not gated: %+v", r)
	}
	if len(f.calls) != 1 {
		t.Fatalf("gated POST reached the runner: %v", f.calls)
	}

	withRisk := `{"service":"lark","argv":["api","POST","/open-apis/drive/v1/export_tasks","--data","{\"file_extension\":\"docx\",\"token\":\"docx_token\",\"type\":\"docx\"}"],"confirm":true,"high_risk":true}`
	if r := p.Call(approved(), "cli_raw", json.RawMessage(withRisk)); !r.OK {
		t.Fatalf("api POST with high_risk failed: %+v error=%+v", r, r.Error)
	}
	if len(f.calls) != 2 || !strings.Contains(f.calls[1], "api POST /open-apis/drive/v1/export_tasks") ||
		!strings.Contains(f.calls[1], `--data={"file_extension":"docx","token":"docx_token","type":"docx"}`) ||
		strings.Contains(f.calls[1], "--yes") {
		t.Fatalf("api POST call = %v", f.calls)
	}
}

func TestRawAPIMethodAndPathBoundaries(t *testing.T) {
	f := &fakeRunner{}
	p := New(Config{}, f)
	cases := []struct{ name, argv, wantCode string }{
		{"delete", `["api","DELETE","/open-apis/drive/v1/files/x"]`, "COMMAND_BLOCKED"},
		{"auth", `["api","GET","/open-apis/auth/v3/tenant_access_token/internal"]`, "ARGUMENT_BLOCKED"},
		{"authen", `["api","POST","/open-apis/authen/v1/access_token"]`, "ARGUMENT_BLOCKED"},
		{"traversal", `["api","GET","/open-apis/../admin"]`, "ARGUMENT_BLOCKED"},
		{"query in path", `["api","GET","/open-apis/calendar/v4/calendars?page=1"]`, "ARGUMENT_BLOCKED"},
		{"unknown method", `["api","TRACE","/open-apis/calendar/v4/calendars"]`, "ARGUMENT_BLOCKED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"service":"lark","argv":` + tc.argv + `,"confirm":true,"high_risk":true}`
			r := p.Call(approved(), "cli_raw", json.RawMessage(body))
			if r.OK || r.Error == nil || r.Error.Code != tc.wantCode {
				t.Fatalf("api call was not blocked: %+v", r)
			}
		})
	}
	if len(f.calls) != 0 {
		t.Fatalf("blocked api calls reached the runner: %v", f.calls)
	}
}

func TestRawAPIOutputStaysInsideWorkspace(t *testing.T) {
	root := chdirForLarkTest(t)
	f := &fakeRunner{}
	p := New(Config{}, f)

	relative := `{"service":"lark","argv":["api","GET","/open-apis/drive/v1/files/x/download","--output","out/result.bin"],"confirm":true}`
	if r := p.Call(approved(), "cli_raw", json.RawMessage(relative)); !r.OK {
		t.Fatalf("relative output failed: %+v error=%+v", r, r.Error)
	}
	absolute := filepath.Join(root, "out", "result.bin")
	if len(f.calls) != 1 || !strings.Contains(f.calls[0], "--output="+absolute) {
		t.Fatalf("output was not rewritten: %v", f.calls)
	}
	if r := p.Call(approved(), "cli_raw", json.RawMessage(relative)); !r.OK || r.OutputPath != absolute {
		t.Fatalf("relative output path not reported: %+v", r)
	}

	placeholder := `{"service":"lark","argv":["api","GET","/open-apis/drive/v1/files/x/download","-o","{file}"],"path":"out/from-placeholder.bin","confirm":true}`
	if r := p.Call(approved(), "cli_raw", json.RawMessage(placeholder)); !r.OK {
		t.Fatalf("placeholder output failed: %+v error=%+v", r, r.Error)
	}
	placeholderPath := filepath.Join(root, "out", "from-placeholder.bin")
	if len(f.calls) != 3 || !strings.Contains(f.calls[2], "--output="+placeholderPath) {
		t.Fatalf("placeholder output was not rewritten: %v", f.calls)
	}

	auto := `{"service":"lark","argv":["api","GET","/open-apis/drive/v1/files/x/download","-o","{file}"],"confirm":true}`
	autoResult := p.Call(approved(), "cli_raw", json.RawMessage(auto))
	if !autoResult.OK {
		t.Fatalf("default {file} output failed: %+v error=%+v", autoResult, autoResult.Error)
	}
	autoPrefix := filepath.Join(root, ".golder", "artifacts", "lark-api-")
	if autoResult.OutputPath == "" || !strings.HasPrefix(autoResult.OutputPath, autoPrefix) {
		t.Fatalf("default output path = %q, want prefix %q", autoResult.OutputPath, autoPrefix)
	}
	if len(f.calls) != 4 || !strings.Contains(f.calls[3], "--output="+autoResult.OutputPath) {
		t.Fatalf("default output was not rewritten: %v", f.calls)
	}

	outside := t.TempDir()
	if err := os.Symlink(filepath.Join(outside, "escape.bin"), filepath.Join(root, "escape.bin")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	blocked := []struct{ name, body string }{
		{"absolute", `{"service":"lark","argv":["api","GET","/open-apis/drive/v1/files/x/download","--output","/etc/passwd"],"confirm":true}`},
		{"parent escape", `{"service":"lark","argv":["api","GET","/open-apis/drive/v1/files/x/download","--output","../outside.bin"],"confirm":true}`},
		{"stdout dash", `{"service":"lark","argv":["api","GET","/open-apis/drive/v1/files/x/download","--output","-"],"confirm":true}`},
		{"final symlink escape", `{"service":"lark","argv":["api","GET","/open-apis/drive/v1/files/x/download","--output","escape.bin"],"confirm":true}`},
		{"path without placeholder", `{"service":"lark","argv":["api","GET","/open-apis/drive/v1/files/x/download"],"path":"out.bin","confirm":true}`},
	}
	for _, tc := range blocked {
		t.Run(tc.name, func(t *testing.T) {
			before := len(f.calls)
			r := p.Call(approved(), "cli_raw", json.RawMessage(tc.body))
			if r.OK || r.Error == nil || len(f.calls) != before {
				t.Fatalf("%s was not blocked: %+v calls=%v", tc.name, r, f.calls[before:])
			}
		})
	}
}

func TestRawAPIDataAndUploadBoundaries(t *testing.T) {
	root := chdirForLarkTest(t)
	if err := os.WriteFile(filepath.Join(root, "body.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &fakeRunner{}
	p := New(Config{}, f)
	blocked := []struct{ name, body string }{
		{"data @file", `{"service":"lark","argv":["api","POST","/open-apis/im/v1/messages","--data","@/etc/passwd"],"confirm":true,"high_risk":true}`},
		{"data stdin", `{"service":"lark","argv":["api","POST","/open-apis/im/v1/messages","--data","-"],"confirm":true,"high_risk":true}`},
		{"params @file", `{"service":"lark","argv":["api","GET","/open-apis/calendar/v4/calendars","--params","@/etc/passwd"],"confirm":true}`},
		{"invalid JSON", `{"service":"lark","argv":["api","POST","/open-apis/im/v1/messages","--data","hello"],"confirm":true,"high_risk":true}`},
		{"upload without placeholder", `{"service":"lark","argv":["api","POST","/open-apis/drive/v1/files/upload_all","--file","/etc/passwd"],"confirm":true,"high_risk":true}`},
		{"controlled flag", `{"service":"lark","argv":["api","GET","/open-apis/calendar/v4/calendars","--format","json"],"confirm":true}`},
		{"unknown flag", `{"service":"lark","argv":["api","GET","/open-apis/calendar/v4/calendars","--header","x: y"],"confirm":true}`},
	}
	for _, tc := range blocked {
		t.Run(tc.name, func(t *testing.T) {
			before := len(f.calls)
			r := p.Call(approved(), "cli_raw", json.RawMessage(tc.body))
			if r.OK || r.Error == nil || len(f.calls) != before {
				t.Fatalf("%s was not blocked: %+v calls=%v", tc.name, r, f.calls[before:])
			}
		})
	}

	upload := `{"service":"lark","argv":["api","POST","/open-apis/drive/v1/files/upload_all","--file","file={file}"],"path":"body.txt","confirm":true,"high_risk":true}`
	if r := p.Call(approved(), "cli_raw", json.RawMessage(upload)); !r.OK {
		t.Fatalf("api upload failed: %+v error=%+v", r, r.Error)
	}
	absolute := filepath.Join(root, "body.txt")
	if len(f.calls) != 1 || !strings.Contains(f.calls[0], "--file=file="+absolute) {
		t.Fatalf("upload path was not rewritten: %v", f.calls)
	}
}

func TestAPIIsOnlyReachableThroughCLIRaw(t *testing.T) {
	f := &fakeRunner{}
	p := New(Config{}, f)
	body := `{"service":"lark","argv":["api","GET","/open-apis/calendar/v4/calendars"],"confirm":true}`
	r := p.Call(approved(), "cli_call", json.RawMessage(body))
	if r.OK || r.Error == nil || r.Error.Code != "COMMAND_BLOCKED" || !strings.Contains(r.Error.NextAction, "cli_raw") {
		t.Fatalf("api through cli_call was not redirected: %+v", r)
	}
	if len(f.calls) != 0 {
		t.Fatalf("cli_call api reached the runner: %v", f.calls)
	}
}

func TestCLIHelpAcceptsTrailingHelpSuffix(t *testing.T) {
	f := &fakeRunner{}
	p := New(Config{}, f)
	for _, command := range [][]string{
		{"drive", "+export", "--help"},
		{"drive", "+export", "-h"},
		{"api", "--help"},
	} {
		body, _ := json.Marshal(map[string]any{"service": "lark", "command": command})
		if r := p.Call(approved(), "cli_help", body); !r.OK {
			t.Fatalf("cli_help %v failed: %+v", command, r)
		}
	}
	want := []string{
		"lark drive +export --help",
		"lark drive +export --help",
		"lark api --help",
	}
	if len(f.calls) != len(want) {
		t.Fatalf("cli_help calls = %v", f.calls)
	}
	for i := range want {
		if f.calls[i] != want[i] {
			t.Fatalf("cli_help call %d = %q, want %q", i, f.calls[i], want[i])
		}
	}
}

func TestDriveExportWithFileExtensionAndOutputDir(t *testing.T) {
	root := chdirForLarkTest(t)
	f := &fakeRunner{}
	p := New(Config{}, f)
	body := `{"operation":"download","options":{"service":"lark","path":".golder/artifacts","argv":["drive","+export","--url","https://tenant.feishu.cn/wiki/wiki_token","--file-extension","docx","--output-dir","{file}","--overwrite"]},"confirm":true}`
	if r := p.Call(approved(), "file_transfer", json.RawMessage(body)); !r.OK {
		t.Fatalf("drive +export failed: %+v error=%+v", r, r.Error)
	}
	output := filepath.Join(root, ".golder", "artifacts")
	if len(f.calls) != 1 || !strings.Contains(f.calls[0], "--file-extension docx") ||
		!strings.Contains(f.calls[0], "--output-dir "+output) {
		t.Fatalf("drive +export call = %v", f.calls)
	}

	withName := `{"operation":"download","options":{"service":"lark","path":".golder/artifacts/out.docx","argv":["drive","+export","--url","https://tenant.feishu.cn/wiki/wiki_token","--file-extension","docx","--file-name","out.docx","--output-dir","{file}"]},"confirm":true}`
	if r := p.Call(approved(), "file_transfer", json.RawMessage(withName)); !r.OK {
		t.Fatalf("drive +export with --file-name failed: %+v error=%+v", r, r.Error)
	}
	if len(f.calls) != 2 || !strings.Contains(f.calls[1], "--file-name out.docx") {
		t.Fatalf("--file-name was rejected or lost: %v", f.calls)
	}
}

func TestFileTransferStillRejectsPathEscapes(t *testing.T) {
	chdirForLarkTest(t)
	f := &fakeRunner{}
	p := New(Config{}, f)
	for _, output := range []string{"/etc/passwd", "../../outside.docx"} {
		body := `{"operation":"download","options":{"service":"lark","path":"out.docx","argv":["drive","+export","--file-extension","docx","--output-dir","` + output + `"]},"confirm":true}`
		r := p.Call(approved(), "file_transfer", json.RawMessage(body))
		if r.OK || r.Error == nil || r.Error.Code != "ARGUMENT_BLOCKED" || len(f.calls) != 0 {
			t.Fatalf("output %q was not rejected: %+v calls=%v", output, r, f.calls)
		}
	}
	// {} in --file-extension is now rejected as a placeholder on a non-path flag.
	body := `{"operation":"download","options":{"service":"lark","path":"out.docx","argv":["drive","+export","--file-extension","{file}","--output-dir","{file}"]},"confirm":true}`
	if r := p.Call(approved(), "file_transfer", json.RawMessage(body)); r.OK || r.Error == nil || !strings.Contains(r.Error.Message, "not a local path parameter") {
		t.Fatalf("file-extension placeholder was not rejected clearly: %+v", r)
	}
}

func chdirForLarkTest(t *testing.T) string {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
