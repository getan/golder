//go:build darwin || linux

package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getan/tuios-mini/mini/control"
)

// This crosses the real embedded daemon wire, not just the dependency's Git
// helper. It fails with tuios-mini v0.10.14 and a held index.lock.
func TestEmbeddedGitPreviewDoesNotNeedTheWritersIndexLock(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	opts := runtimeOptions(t)
	// A caller's alternate index, object directory, template or injected Git
	// config must never retarget this fixture at a real repository.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GIT_") {
			t.Setenv(key, "") // register restoration of the caller's environment
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", opts.Cwd}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git fixture %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	file := filepath.Join(opts.Cwd, "change.txt")
	if err := os.WriteFile(file, []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "--", "change.txt")
	git("-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-q", "-m", "base")
	if err := os.WriteFile(file, []byte("staged snapshot\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "--", "change.txt")
	if err := os.WriteFile(file, []byte("working snapshot\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(opts.Cwd, ".git", "index")
	before, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	lock := index + ".lock"
	marker := []byte("owned integration fixture writer\n")
	if err := os.WriteFile(lock, marker, 0o600); err != nil {
		t.Fatal(err)
	}
	backend := &Backend{Version: "git-preview-lock-test"}
	t.Cleanup(backend.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err = backend.Start(ctx, opts); err != nil {
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
	if json.Unmarshal(raw, &windows) != nil || len(windows.Windows) == 0 {
		t.Fatalf("windows: %s", raw)
	}
	window := windows.Windows[0].ID
	check := func(client *control.Client, mode string) error {
		raw, err := client.Call(ctx, "review-diff", map[string]any{"session": opts.Session, "window": window, "index": mode, "paths": []string{"change.txt"}})
		if err != nil {
			return fmt.Errorf("%s preview under active index.lock: %w", mode, err)
		}
		var result struct {
			Files []struct {
				Path  string `json:"path"`
				Hunks []struct {
					Lines []struct {
						Op   string `json:"op"`
						Text string `json:"text"`
					} `json:"lines"`
				} `json:"hunks"`
			} `json:"files"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return err
		}
		want := "staged snapshot"
		if mode == "unstaged" {
			want = "working snapshot"
		}
		for _, file := range result.Files {
			if file.Path != "change.txt" {
				continue
			}
			for _, h := range file.Hunks {
				for _, line := range h.Lines {
					if line.Op == "add" && line.Text == want {
						return nil
					}
				}
			}
		}
		return fmt.Errorf("%s preview lost expected content %q: %s", mode, want, raw)
	}
	for _, mode := range []string{"staged", "unstaged"} {
		if err := check(ctl, mode); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	failures := make(chan error, 4)
	for i := range 4 {
		wg.Go(func() {
			client, err := control.Dial(ctx, opts.Socket)
			if err != nil {
				failures <- err
				return
			}
			defer client.Close()
			mode := "staged"
			if i%2 != 0 {
				mode = "unstaged"
			}
			if err := check(client, mode); err != nil {
				failures <- err
			}
		})
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	after, err := os.ReadFile(index)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("preview modified the real index", err)
	}
	kept, err := os.ReadFile(lock)
	if err != nil || !bytes.Equal(marker, kept) {
		t.Fatal("preview changed or removed the active lock", err)
	}
	working, err := os.ReadFile(file)
	if err != nil || strings.TrimSpace(string(working)) != "working snapshot" {
		t.Fatal("preview changed working files", err)
	}
	t.Logf("staged/unstaged wire previews passed; real_index_sha256=%x lock_preserved=true", sha256.Sum256(after))
}
