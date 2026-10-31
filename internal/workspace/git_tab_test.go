//go:build darwin || linux

package workspace

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getan/tuios-mini/mini/control"
)

// Cross the actual embedded daemon dispatcher. Calling a Git helper directly
// missed the registry rejecting ref on every second-level Git drawer. Also
// exercise the mutation verbs using only a throwaway repository/local remote.
func TestEmbeddedGitTabWireActions(t *testing.T) {
	opts := runtimeOptions(t)
	// Keep persistent workspace state outside the Git worktree.
	opts.Cwd = t.TempDir()
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GIT_") {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", opts.Cwd}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(opts.Cwd, "file.txt"), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q")
	git("config", "user.name", "fixture")
	git("config", "user.email", "fixture@example.invalid")
	write("base\n")
	git("add", "--", "file.txt")
	git("commit", "-qm", "git tab fixture")
	git("branch", "drawer-branch")
	git("tag", "drawer-tag")
	write("stashed content\n")
	git("stash", "push", "-qm", "drawer-stash")
	remote := filepath.Join(t.TempDir(), "remote.git")
	git("init", "--bare", "-q", remote)
	git("remote", "add", "origin", remote)
	git("push", "-qu", "origin", "HEAD")

	backend := &Backend{Version: "git-tab-test"}
	t.Cleanup(backend.Close)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := backend.Start(ctx, opts); err != nil {
		t.Fatal(err)
	}
	ctl, err := control.Dial(ctx, opts.Socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ctl.Close() })
	raw, err := ctl.Call(ctx, "list-windows", map[string]any{"session": opts.Session})
	if err != nil {
		t.Fatal(err)
	}
	var windows struct {
		Windows []struct {
			ID string `json:"window_id"`
		} `json:"windows"`
	}
	if err := json.Unmarshal(raw, &windows); err != nil || len(windows.Windows) != 1 {
		t.Fatalf("windows: %s (%v)", raw, err)
	}
	call := func(t *testing.T, verb string, params map[string]any) json.RawMessage {
		t.Helper()
		params["session"], params["window"] = opts.Session, windows.Windows[0].ID
		raw, err := ctl.Call(ctx, verb, params)
		if err != nil {
			t.Fatalf("%s %v: %v", verb, params, err)
		}
		return raw
	}
	for _, kind := range []string{"commits", "graph", "branches", "tags", "stashes", "remotes", "file", "repos"} {
		t.Run("list/"+kind, func(t *testing.T) {
			params := map[string]any{"kind": kind, "limit": 100}
			if kind == "file" {
				params["path"] = "file.txt"
			}
			var res struct{ Rows []string }
			raw := call(t, "review-history", params)
			if err := json.Unmarshal(raw, &res); err != nil || len(res.Rows) == 0 {
				t.Fatalf("empty %s: %s (%v)", kind, raw, err)
			}
		})
	}
	for _, detail := range []struct{ kind, ref, want string }{
		{"commit", "HEAD", "git tab fixture"},
		{"branch", "drawer-branch", "git tab fixture"},
		{"tag", "drawer-tag", "git tab fixture"},
		{"stash", "stash@{0}", "stashed content"},
		{"remote", "origin", remote},
	} {
		t.Run("detail/"+detail.kind, func(t *testing.T) {
			raw := call(t, "review-history", map[string]any{"kind": detail.kind, "ref": detail.ref, "limit": 500})
			if !strings.Contains(string(raw), detail.want) {
				t.Fatalf("detail missing %q: %s", detail.want, raw)
			}
		})
	}
	write("staged content\n")
	call(t, "review-stage", map[string]any{"action": "stage", "path": "file.txt"})
	if got := git("show", ":file.txt"); got != "staged content" {
		t.Fatalf("stage = %q", got)
	}
	call(t, "review-stage", map[string]any{"action": "unstage", "path": "file.txt"})
	if got := git("diff", "--cached", "--name-only"); got != "" {
		t.Fatalf("unstage left %q", got)
	}
	call(t, "review-stage", map[string]any{"action": "stage", "path": "file.txt"})
	write("working content\n")
	for _, side := range []string{"staged", "unstaged"} {
		call(t, "review-diff", map[string]any{"index": side, "paths": []string{"file.txt"}})
	}
	call(t, "review-commit", map[string]any{"message": "git tab staged commit"})
	if got := git("show", "HEAD:file.txt"); got != "staged content" {
		t.Fatalf("commit included unstaged changes: %q", got)
	}
	call(t, "review-discard", map[string]any{"paths": []string{"file.txt"}})
	if got := git("status", "--porcelain"); got != "" {
		t.Fatalf("discard left changes: %q", got)
	}
	call(t, "review-sync", map[string]any{})
	if git("rev-parse", "HEAD") != git("rev-parse", "@{upstream}") {
		t.Fatal("sync did not push the staged commit to the local remote")
	}
	t.Log("Git tab wire: 8 lists, 5 details, stage/unstage, staged/unstaged diff, index-only commit, discard and local-remote sync passed")
}
