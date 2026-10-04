//go:build linux

package seatbelt

// End-to-end confinement test for the bubblewrap runner: it exercises the real
// bwrap process (mounts, namespaces) rather than argv construction. Skips
// cleanly when bubblewrap is absent or unusable, so it is safe on any runner.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBwrapRunnerEndToEnd(t *testing.T) {
	if !Available() {
		t.Skip("bubblewrap not available or unusable on this machine")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	golderHome := filepath.Join(home, "ghome")
	if err := os.MkdirAll(golderHome, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOLDER_HOME", golderHome)
	trust := filepath.Join(golderHome, "trust.json")
	if err := os.WriteFile(trust, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The project IS $GOLDER_HOME: the writable project bind covers the trust
	// store, so this setup proves the secret re-assert ordering.
	// TmpDir is pinned to its own directory (not os.TempDir()): the "outside"
	// probe below lives under the process TMPDIR too, and binding all of it
	// would make that probe meaningless.
	r := &BwrapRunner{ProjectDir: golderHome, TmpDir: t.TempDir()}
	run := func(command string) (string, error) {
		argv, cleanup, err := r.SandboxArgv("/bin/bash", "-c", command, golderHome)
		if err != nil {
			t.Fatalf("SandboxArgv: %v", err)
		}
		if cleanup != nil {
			defer cleanup()
		}
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir = golderHome
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	if out, err := run("echo ok > note.txt"); err != nil {
		t.Fatalf("write inside the project must succeed: %v\n%s", err, out)
	}
	if _, err := run("cat /etc/hostname"); err != nil {
		t.Errorf("reads outside the project must stay allowed: %v", err)
	}
	outside := t.TempDir()
	if out, err := run("echo x > " + filepath.Join(outside, "x")); err == nil {
		t.Errorf("write outside the project must fail:\n%s", out)
	}
	if out, err := run("echo hacked > " + trust); err == nil {
		t.Errorf("the trust store must stay read-only inside a writable tree:\n%s", out)
	}
	// Reading secret material is denied too (codex parity): a masked tmpfs
	// hides the file entirely, so `cat` fails and the content never appears.
	if out, err := run("cat " + trust); err == nil {
		t.Errorf("reading the trust store must be denied inside the sandbox:\n%s", out)
	} else if strings.Contains(out, "{}") {
		t.Errorf("secret content leaked out of the sandbox:\n%s", out)
	}
	if _, err := run("echo hi"); err != nil {
		t.Errorf("plain commands must work: %v", err)
	}

	// The read model is a whitelist: credential material under $HOME is not
	// mounted at all, so it is unreachable; the git config files are mounted so
	// that `git commit` can find an identity.
	secret := filepath.Join(home, ".zshrc")
	if err := os.WriteFile(secret, []byte("export API_KEY=leaked"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := run("cat " + secret); err == nil {
		t.Errorf("reading $HOME/.zshrc must be denied inside the sandbox:\n%s", out)
	} else if strings.Contains(out, "leaked") {
		t.Errorf("shell rc content leaked out of the sandbox:\n%s", out)
	}
	gitconfig := filepath.Join(home, ".gitconfig")
	if err := os.WriteFile(gitconfig, []byte("[user]\n\temail = probe@example.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := run("cat " + gitconfig); err != nil {
		t.Errorf("the git config must stay readable (git commit needs it): %v\n%s", err, out)
	} else if !strings.Contains(out, "probe@example.com") {
		t.Errorf("git config read returned unexpected content:\n%s", out)
	}
}

// TestBwrapRunnerNetworkOff proves the default sandbox has no egress: an
// outbound connection attempt fails. (GOLDER_SANDBOX_NETWORK=on is exercised
// by the argv-level test, which checks --share-net appears.)
func TestBwrapRunnerNetworkOff(t *testing.T) {
	// The table above is driven by NetworkEnabled(), which reads the ambient
	// environment; force the default here so the test is deterministic.
	t.Setenv("GOLDER_SANDBOX_NETWORK", "")
	if !Available() {
		t.Skip("bubblewrap not available or unusable on this machine")
	}
	project := t.TempDir()
	t.Setenv("GOLDER_HOME", filepath.Join(t.TempDir(), "ghome"))
	r := &BwrapRunner{ProjectDir: project}
	// bash's /dev/tcp needs no external binary; a refused/unreachable connect
	// fails fast. If name resolution or connect succeeds the sandbox leaked.
	out, err := runBwrap(t, r, project, "exec 3<>/dev/tcp/1.1.1.1/80 && echo CONNECTED || echo BLOCKED")
	if err != nil {
		t.Fatalf("probe command failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "BLOCKED") {
		t.Errorf("network should be off inside the sandbox, got:\n%s", out)
	}
}

// runBwrap executes a command under the runner and returns combined output.
func runBwrap(t *testing.T, r Runner, dir, command string) (string, error) {
	t.Helper()
	argv, cleanup, err := r.SandboxArgv("/bin/bash", "-c", command, dir)
	if err != nil {
		t.Fatalf("SandboxArgv: %v", err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestBwrapRunnerEndToEndTTYShape(t *testing.T) {
	if !Available() {
		t.Skip("bubblewrap not available or unusable on this machine")
	}
	project := t.TempDir()
	t.Setenv("GOLDER_HOME", filepath.Join(t.TempDir(), "ghome"))
	r := &BwrapRunner{ProjectDir: project}
	// bubblewrap's --dev mounts a fresh devtmpfs including devpts, so TTY
	// sessions keep working; check the devpts mountpoint and the ptmx link.
	argv, cleanup, err := r.SandboxArgv("/bin/bash", "-c", "test -d /dev/pts && test -e /dev/ptmx", project)
	if err != nil {
		t.Fatalf("SandboxArgv: %v", err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = project
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("devpts must exist inside the sandbox for TTY sessions: %v\n%s", err, out)
	}
}
