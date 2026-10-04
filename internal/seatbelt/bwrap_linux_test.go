//go:build linux

package seatbelt

// End-to-end confinement test for the bubblewrap runner: it exercises the real
// bwrap process (mounts, namespaces) rather than argv construction. Skips
// cleanly when bubblewrap is absent or unusable, so it is safe on any runner.

import (
	"os"
	"os/exec"
	"path/filepath"
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
	if _, err := run("echo hi"); err != nil {
		t.Errorf("plain commands must work: %v", err)
	}
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
