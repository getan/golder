package mobilegit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	"github.com/go-git/go-git/v5/plumbing/transport/server"

	"github.com/getan/golder/internal/agentcore"
)

func run(t *testing.T, tool *Tool, ctx context.Context, args string) (string, bool) {
	t.Helper()
	result, err := tool.Execute(ctx, "test", json.RawMessage(args), nil)
	if err != nil {
		t.Fatalf("Execute(%s): %v", args, err)
	}
	return agentcore.ContentToText(result.Content), result.IsError
}

func TestLocalGitWorkflow(t *testing.T) {
	root := t.TempDir()
	tool, err := New(Config{Root: root, AuthorName: "Golder Test", AuthorEmail: "test@example.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if out, bad := run(t, tool, ctx, `{"action":"status"}`); !bad || !strings.Contains(out, "not a Git repository") {
		t.Fatalf("status before init = %q, bad=%t", out, bad)
	}
	if out, bad := run(t, tool, ctx, `{"action":"init"}`); bad {
		t.Fatalf("init: %s", out)
	}
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, bad := run(t, tool, ctx, `{"action":"status"}`)
	if bad || !strings.Contains(out, "hello.txt") {
		t.Fatalf("status = %q, bad=%t", out, bad)
	}
	if out, bad := run(t, tool, ctx, `{"action":"add","path":"hello.txt"}`); bad {
		t.Fatalf("add: %s", out)
	}
	out, bad = run(t, tool, ctx, `{"action":"commit","message":"initial commit"}`)
	if bad || !strings.Contains(out, "Committed") {
		t.Fatalf("commit = %q, bad=%t", out, bad)
	}
	if out, bad := run(t, tool, ctx, `{"action":"status"}`); bad || !strings.Contains(out, "working tree clean") {
		t.Fatalf("clean status = %q, bad=%t", out, bad)
	}
	if out, bad := run(t, tool, ctx, `{"action":"log"}`); bad || !strings.Contains(out, "initial commit") {
		t.Fatalf("log = %q, bad=%t", out, bad)
	}
	if out, bad := run(t, tool, ctx, `{"action":"branch","name":"feature"}`); bad || !strings.Contains(out, "feature") {
		t.Fatalf("branch create = %q, bad=%t", out, bad)
	}
	if out, bad := run(t, tool, ctx, `{"action":"branch"}`); bad || !strings.Contains(out, "feature") {
		t.Fatalf("branch list = %q, bad=%t", out, bad)
	}
	if out, bad := run(t, tool, ctx, `{"action":"remote"}`); bad || !strings.Contains(out, "No remotes") {
		t.Fatalf("remote = %q, bad=%t", out, bad)
	}
	if out, bad := run(t, tool, ctx, `{"action":"diff"}`); bad || !strings.Contains(out, "no parent diff") {
		t.Fatalf("diff = %q, bad=%t", out, bad)
	}
	repo, err := git.PlainOpen(root)
	if err != nil {
		t.Fatal(err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		t.Fatal(err)
	}
	if commit.Author.Name != "Golder Test" || commit.Author.Email != "test@example.invalid" {
		t.Fatalf("author = %+v", commit.Author)
	}
}

func TestGitNetworkGateAndPathEscape(t *testing.T) {
	tool, err := New(Config{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	out, bad := run(t, tool, context.Background(), `{"action":"clone","url":"https://example.invalid/repo.git"}`)
	if !bad || !strings.Contains(out, "network access is not authorized") {
		t.Fatalf("ungranted clone = %q, bad=%t", out, bad)
	}
	granted := agentcore.WithNetworkGrant(context.Background())
	if out, bad := run(t, tool, granted, `{"action":"clone","url":"https://example.invalid/repo.git","path":"../escape"}`); !bad || !strings.Contains(out, "escapes the workspace") {
		t.Fatalf("escaping clone = %q, bad=%t", out, bad)
	}
}

func TestGitFileRemoteRoundTrip(t *testing.T) {
	client.InstallProtocol("file", server.NewClient(server.DefaultLoader))
	remoteDir := filepath.Join(t.TempDir(), "remote.git")
	remote, err := git.PlainInit(remoteDir, true)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	tool, err := New(Config{Root: root, AuthorName: "Golder Test", AuthorEmail: "test@example.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := agentcore.WithNetworkGrant(context.Background())
	if out, bad := run(t, tool, ctx, `{"action":"init"}`); bad {
		t.Fatalf("init: %s", out)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, tool, ctx, `{"action":"add","all":true}`)
	if out, bad := run(t, tool, ctx, `{"action":"commit","message":"fixture"}`); bad {
		t.Fatalf("commit: %s", out)
	}
	repo, err := git.PlainOpen(root)
	if err != nil {
		t.Fatal(err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, head.Name())); err != nil {
		t.Fatal(err)
	}
	remoteURL := "file://" + remoteDir
	if _, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{remoteURL}}); err != nil {
		t.Fatal(err)
	}
	if out, bad := run(t, tool, ctx, `{"action":"push"}`); bad {
		t.Fatalf("push: %s", out)
	}
	cloneRoot := t.TempDir()
	cloneTool, err := New(Config{Root: cloneRoot})
	if err != nil {
		t.Fatal(err)
	}
	if out, bad := run(t, cloneTool, ctx, `{"action":"clone","url":"`+remoteURL+`"}`); bad {
		t.Fatalf("clone: %s", out)
	}
	raw, err := os.ReadFile(filepath.Join(cloneRoot, "a.txt"))
	if err != nil || string(raw) != "a\n" {
		t.Fatalf("cloned content = %q, %v", raw, err)
	}
}
