package larkmcp

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNativeRunnerRunInUsesExplicitDirAndPrivateHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/sh is not available on Windows")
	}
	home := t.TempDir()
	work := t.TempDir()
	resolvedWork, err := filepath.EvalSymlinks(work)
	if err != nil {
		t.Fatal(err)
	}
	runner := NativeRunner{Config: Config{LarkCLI: "/bin/sh", Home: home, ProjectHost: "project.feishu.cn"}}
	out, err := runner.RunIn(context.Background(), "lark", work, []string{"-c", `pwd; printf '\nHOME=%s\n' "$HOME"`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := string(out.Stdout)
	if !strings.Contains(text, resolvedWork) {
		t.Fatalf("CLI did not run in the explicit dir: %q", text)
	}
	if !strings.Contains(text, "HOME="+home) {
		t.Fatalf("CLI lost the private HOME: %q", text)
	}
}

func TestNativeRunnerRejectsRelativeWorkingDirectory(t *testing.T) {
	runner := NativeRunner{Config: Config{LarkCLI: "/bin/sh", Home: t.TempDir()}}
	if _, err := runner.RunIn(context.Background(), "lark", "relative", []string{"-c", "true"}, nil); err == nil {
		t.Fatal("relative CLI working directory was accepted")
	}
}

func TestFileTransferRunsAtResolvedProjectRoot(t *testing.T) {
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRunner{}
	p := New(Config{}, f)
	call := []byte(`{"operation":"download","options":{"service":"lark","path":"papers/Qwen-VL.pdf","argv":["im","+messages-resources-download","--message-id","om_x100b7f625c10e074b4aa3ee9ac74b3c","--file-key","file_v3_0017o_4614a9c1-556f-427e-a136-0cae9397995g","--output","{file}"]},"confirm":true}`)
	if r := p.Call(approved(), "file_transfer", call); !r.OK {
		t.Fatalf("download failed: %+v", r)
	}
	if len(f.dirs) != 1 || f.dirs[0] != resolvedRoot {
		t.Fatalf("lark-cli cwd = %v, want %q", f.dirs, resolvedRoot)
	}
}
