package tui

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestGitStatusProbeDoesNotRefreshTheSharedIndex(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	// No caller-supplied Git repository/index, no personal configuration.
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE"} {
		if value, ok := os.LookupEnv(key); ok {
			t.Setenv(key, value)
			_ = os.Unsetenv(key)
		}
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_OPTIONAL_LOCKS", "1") // the reader must override an ambient opt-in
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	file := filepath.Join(repo, "file")
	if err := os.WriteFile(file, []byte("unchanged content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "--", "file")
	index := filepath.Join(repo, ".git", "index")
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(10 * time.Second)
	if err = os.Chtimes(file, future, future); err != nil {
		t.Fatal(err)
	}
	if _, err = runGit(repo, "status", "--porcelain", "-b"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("background status rewrote the shared index and could contend with a writer")
	}
	// A control confirms that this filesystem/fixture really exposes a refresh.
	git("status", "--porcelain", "-b")
	control, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(before, control) {
		t.Skip("Git did not expose a stat-cache refresh on this filesystem")
	}
}
