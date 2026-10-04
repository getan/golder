package seatbelt

// Tests for the bubblewrap argv construction. They run on any platform (the
// builder is platform-neutral); the real end-to-end confinement test is in
// bwrap_linux_test.go.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// indexOfSeq returns the index of the first occurrence of the seq in argv, or
// -1. It is used to assert relative ordering of mounts.
func indexOfSeq(argv []string, seq ...string) int {
	for i := 0; i+len(seq) <= len(argv); i++ {
		match := true
		for j, s := range seq {
			if argv[i+j] != s {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func TestBwrapRunnerArgv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GOLDER_HOME", filepath.Join(home, "ghome"))
	project := t.TempDir()
	tmp := t.TempDir()
	r := &BwrapRunner{ProjectDir: project, TmpDir: tmp}

	argv, cleanup, err := r.SandboxArgv("/bin/bash", "-c", "echo hi", project)
	if err != nil {
		t.Fatalf("SandboxArgv: %v", err)
	}
	if cleanup != nil {
		cleanup() // bwrap generates no profile; a non-nil cleanup must still be safe
	}
	if argv[0] != "bwrap" {
		t.Fatalf("argv[0] = %q, want bwrap", argv[0])
	}
	if got, want := argv[len(argv)-3:], []string{"/bin/bash", "-c", "echo hi"}; !equalSlices(got, want) {
		t.Fatalf("argv tail = %v, want %v", got, want)
	}
	for _, want := range []string{
		"--die-with-parent", "--unshare-pid", "--dev", "--proc",
	} {
		if indexOfSeq(argv, want) < 0 {
			t.Errorf("argv missing %s: %v", want, argv)
		}
	}
	if indexOfSeq(argv, "--ro-bind", "/", "/") < 0 {
		t.Errorf("argv missing the read-only root bind: %v", argv)
	}
	// Network stays open and the session is not detached from the terminal:
	// unsharing net would break the tools, --new-session would break TTY use.
	for _, banned := range []string{"--unshare-net", "--new-session", "--unshare-all"} {
		if indexOfSeq(argv, banned) >= 0 {
			t.Errorf("argv must not carry %s: %v", banned, argv)
		}
	}
	// The project and tmp dirs are writable...
	if indexOfSeq(argv, "--bind", project, project) < 0 {
		t.Errorf("argv missing the project bind: %v", argv)
	}
	if indexOfSeq(argv, "--bind-try", tmp, tmp) < 0 {
		t.Errorf("argv missing the tmp bind: %v", argv)
	}
	// ...and the secret re-asserts come after, because later mounts win.
	ssh := filepath.Join(home, ".ssh")
	trust := filepath.Join(home, "ghome", "trust.json")
	writable := indexOfSeq(argv, "--bind", project, project)
	sshAt := indexOfSeq(argv, "--ro-bind-try", ssh, ssh)
	trustAt := indexOfSeq(argv, "--ro-bind-try", trust, trust)
	credsAt := indexOfSeq(argv, "--ro-bind-try", filepath.Join(home, "ghome", ".credentials.yaml"))
	if sshAt < 0 || trustAt < 0 || credsAt < 0 {
		t.Fatalf("argv missing secret re-asserts (ssh=%d trust=%d creds=%d): %v", sshAt, trustAt, credsAt, argv)
	}
	if sshAt < writable || trustAt < writable {
		t.Errorf("secret re-asserts must come after the writable bind: %v", argv)
	}
}

func TestBwrapRunnerSymlinkedProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GOLDER_HOME", filepath.Join(home, "ghome"))
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	r := &BwrapRunner{ProjectDir: link, TmpDir: t.TempDir()}
	argv, _, err := r.SandboxArgv("/bin/sh", "-c", "true", link)
	if err != nil {
		t.Fatalf("SandboxArgv: %v", err)
	}
	// Both the symlink form and the resolved form are bound: bubblewrap mounts
	// by path, so a command resolving through either form must find it writable.
	if indexOfSeq(argv, "--bind", link, link) < 0 {
		t.Errorf("argv missing the symlink-form project bind: %v", argv)
	}
	// The resolved form is the fully canonical path (/var → /private/var on
	// macOS), which is what EvalSymlinks yields.
	resolved, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	if indexOfSeq(argv, "--bind", resolved, resolved) < 0 {
		t.Errorf("argv missing the resolved-form project bind: %v", argv)
	}
}

func TestBwrapRunnerUsesCwdWhenProjectEmpty(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GOLDER_HOME", "")
	r := &BwrapRunner{TmpDir: t.TempDir()}
	argv, _, err := r.SandboxArgv("/bin/sh", "-c", "true", cwd)
	if err != nil {
		t.Fatalf("SandboxArgv: %v", err)
	}
	if indexOfSeq(argv, "--bind", cwd, cwd) < 0 {
		t.Errorf("argv should bind the working directory when ProjectDir is empty: %v", argv)
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestBwrapSecretPaths guards the re-derivation of the protected locations
// (the package deliberately does not import the owning packages).
func TestBwrapSecretPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GOLDER_HOME", "")
	joined := strings.Join(secretPaths(), "\n")
	for _, want := range []string{
		filepath.Join(home, ".ssh"),
		filepath.Join(home, ".gnupg"),
		filepath.Join(home, ".golder", "trust.json"),
		filepath.Join(home, ".golder", ".credentials.yaml"),
		filepath.Join(home, ".golder", "sessions"),
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("secretPaths missing %s:\n%s", want, joined)
		}
	}
	// GOLDER_HOME relocates the golder-owned secrets.
	custom := filepath.Join(home, "custom")
	t.Setenv("GOLDER_HOME", custom)
	joined = strings.Join(secretPaths(), "\n")
	if !strings.Contains(joined, filepath.Join(custom, "trust.json")) {
		t.Errorf("secretPaths must follow GOLDER_HOME:\n%s", joined)
	}
}
