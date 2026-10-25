//go:build darwin || linux

package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestE2ENamedCatalogIsGlobalAndDaemonsAreIndependent(t *testing.T) {
	bin := golderBinary(t)
	root := tempRoot(t)
	env := isolatedEnv(t, root)
	firstDir := project(t, root)
	otherDir := filepath.Join(root, "other-project")
	if err := os.MkdirAll(otherDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopQuietly(bin, otherDir, env)
		stopQuietly(bin, otherDir, env, "-s", "trading")
	})
	first := startGolder(t, bin, firstDir, env,
		"workspace", "--headless", "--shell", "--socket", filepath.Join(root, "d.sock"))
	var firstReady readinessLine
	if err := json.Unmarshal([]byte(first.nextLine(t)), &firstReady); err != nil {
		t.Fatal(err)
	}
	if firstReady.Session != "default" {
		t.Fatalf("default session = %+v", firstReady)
	}
	second := startGolder(t, bin, firstDir, env, "workspace", "new", "trading", "--headless", "--shell",
		"-C", otherDir, "--socket", filepath.Join(root, "t.sock"))
	second.nextLine(t)

	// No repeated cwd or socket flags: any directory addresses the same name.
	out, stderr, code := runCLI(t, bin, otherDir, env, "workspace", "--headless")
	if code != 0 {
		t.Fatalf("reuse from other directory: %s", stderr)
	}
	var reused struct {
		Created bool   `json:"created"`
		Cwd     string `json:"cwd"`
		Socket  string `json:"socket"`
	}
	if json.Unmarshal([]byte(out), &reused) != nil || reused.Created || reused.Cwd != firstDir || reused.Socket != firstReady.Socket {
		t.Fatalf("reuse changed the session: %s", out)
	}
	out, stderr, code = runCLI(t, bin, otherDir, env, "workspace", "list", "--json")
	if code != 0 {
		t.Fatalf("list: %s", stderr)
	}
	var catalog struct {
		Sessions []struct {
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"sessions"`
	}
	if json.Unmarshal([]byte(out), &catalog) != nil || len(catalog.Sessions) != 2 {
		t.Fatalf("global list = %s", out)
	}
	for _, s := range catalog.Sessions {
		if s.State != "running" {
			t.Fatalf("named daemon not running: %s", out)
		}
	}
	if _, _, code := runCLI(t, bin, otherDir, env, "workspace", "new", "trading", "--headless"); code == 0 {
		t.Fatal("new accepted an existing name")
	}
	if _, _, code := runCLI(t, bin, otherDir, env, "workspace", "rm"); code == 0 {
		t.Fatal("rm accepted a running session")
	}
	if _, stderr, code := runCLI(t, bin, otherDir, env, "workspace", "stop"); code != 0 {
		t.Fatalf("stop default: %s", stderr)
	}
	if err := first.wait(t, 15*time.Second); err != nil {
		t.Fatal(err)
	}
	out, stderr, code = runCLI(t, bin, firstDir, env, "workspace", "status", "trading")
	if code != 0 {
		t.Fatalf("status trading: %s", stderr)
	}
	trading := parseStatus(t, out)
	if !trading.Running || trading.PID == first.cmd.Process.Pid || trading.PID != second.cmd.Process.Pid {
		t.Fatalf("stopping default affected another daemon: %s", out)
	}
	if _, stderr, code := runCLI(t, bin, otherDir, env, "workspace", "rm"); code != 0 {
		t.Fatalf("remove stopped default: %s", stderr)
	}
	if _, err := os.Stat(filepath.Join(firstDir, "README.md")); err != nil {
		t.Fatal("rm removed project files")
	}
	if _, stderr, code := runCLI(t, bin, otherDir, env, "workspace", "stop", "trading"); code != 0 {
		t.Fatalf("stop trading: %s", stderr)
	}
	if err := second.wait(t, 15*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestE2EAttachMissingDoesNotCreateState(t *testing.T) {
	bin := golderBinary(t)
	root := tempRoot(t)
	env := isolatedEnv(t, root)
	dir := project(t, root)
	client := newTerminal(t).start(t, bin, dir, env, "workspace", "attach", "missing")
	if err := client.waitExit(t, 10*time.Second); err == nil {
		t.Fatal("attach to a missing daemon succeeded")
	}
	if !strings.Contains(client.output(), "attach never creates") || strings.Contains(client.output(), "\x1b[?1049h") {
		t.Fatalf("missing attach started a client/daemon: %q", client.output())
	}
	if _, err := os.Stat(filepath.Join(root, "golder", "workspace")); !os.IsNotExist(err) {
		t.Fatalf("attach created a catalog: %v", err)
	}
}
