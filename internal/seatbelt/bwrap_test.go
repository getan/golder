package seatbelt

// Tests for the bubblewrap argv construction. They run on any platform (the
// builder is platform-neutral); the real end-to-end confinement test is in
// bwrap_linux_test.go.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/getan/golder/internal/permissions"
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
	// The root is NOT bound wholesale (that would expose all of $HOME); the
	// whitelist below is what makes the sandbox a whitelist.
	if indexOfSeq(argv, "--ro-bind", "/", "/") >= 0 {
		t.Errorf("argv must not bind the whole host root: %v", argv)
	}
	// The network is off by default (codex parity), and the session is not
	// detached from the terminal: --new-session would break TTY use.
	if indexOfSeq(argv, "--unshare-net") < 0 {
		t.Errorf("argv should unshare the network by default: %v", argv)
	}
	for _, banned := range []string{"--share-net", "--new-session", "--unshare-all"} {
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
	// Credential material under $HOME is not mounted at all (whitelist), so a
	// read of ~/.ssh cannot resolve. Golder's own state is still masked with an
	// empty tmpfs / a read-only /dev/null AFTER the writable binds, because a
	// project rooted inside $GOLDER_HOME would otherwise inherit writable
	// access to it (later mounts win).
	ssh := filepath.Join(home, ".ssh")
	if indexOfSeq(argv, "--ro-bind", ssh, ssh) >= 0 || indexOfSeq(argv, "--bind", ssh, ssh) >= 0 {
		t.Errorf("argv must not mount $HOME/.ssh: %v", argv)
	}
	golderHome := filepath.Join(home, "ghome")
	trust := filepath.Join(golderHome, "trust.json")
	sessions := filepath.Join(golderHome, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(trust, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	argv, _, err = r.SandboxArgv("/bin/bash", "-c", "echo hi", project)
	if err != nil {
		t.Fatalf("SandboxArgv with existing golder state: %v", err)
	}
	trustAt := indexOfSeq(argv, "--ro-bind", "/dev/null", trust)
	sessionsAt := indexOfSeq(argv, "--tmpfs", sessions)
	writable := indexOfSeq(argv, "--bind", project, project)
	if trustAt < 0 || sessionsAt < 0 {
		t.Fatalf("argv missing golder-state masks (trust=%d sessions=%d): %v", trustAt, sessionsAt, argv)
	}
	if trustAt < writable || sessionsAt < writable {
		t.Errorf("golder-state masks must come after the writable bind: %v", argv)
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

// TestReadableRootsIsAWhitelist locks the whitelist shape: the system runtime
// is readable, and $HOME is NOT — credential material under home is excluded
// by construction rather than by a growing blacklist.
func TestReadableRootsIsAWhitelist(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GOLDER_HOME", "")
	t.Setenv("GOLDER_SANDBOX_READABLE", "")
	// Read grants are process-wide and feed this list too (see the test below);
	// clear them so this test states the baseline whitelist.
	t.Cleanup(func() { permissions.SetReadRoots(nil) })
	permissions.SetReadRoots(nil)

	roots := ReadableRoots()
	for _, want := range []string{"/usr", "/bin", "/opt", "/etc"} {
		if !slices.Contains(roots, want) {
			t.Errorf("ReadableRoots missing system path %s: %v", want, roots)
		}
	}
	// The git config files are the only $HOME entries allowed: identity and the
	// ignore list are needed for commits, everything else under home is out.
	for _, r := range roots {
		if !strings.HasPrefix(r, home) {
			continue
		}
		rel, err := filepath.Rel(home, r)
		if err != nil {
			t.Fatalf("Rel(%s, %s): %v", home, r, err)
		}
		switch rel {
		case ".gitconfig", ".config/git/ignore", ".config/git/attributes":
			// expected
		default:
			t.Errorf("ReadableRoots must not include $HOME/%s: %v", rel, roots)
		}
	}
}

// TestReadableRootsFromEnv covers GOLDER_SANDBOX_READABLE: absolute paths are
// appended (colon- or comma-separated), relative entries are dropped so a
// typo cannot silently widen the sandbox.
func TestReadableRootsFromEnv(t *testing.T) {
	t.Setenv("GOLDER_SANDBOX_READABLE", "/Volumes/KIOXIA:relative/path,/opt/custom")
	roots := ReadableRoots()
	for _, want := range []string{"/Volumes/KIOXIA", "/opt/custom"} {
		if !slices.Contains(roots, want) {
			t.Errorf("ReadableRoots missing env-added %s: %v", want, roots)
		}
	}
	for _, r := range roots {
		if r == "relative/path" {
			t.Errorf("relative env entry must be dropped: %v", roots)
		}
	}
}

// TestSessionReadGrantWidensSandbox pins the wiring that keeps the tools and
// the sandbox telling the same story: a directory the user allowed the
// read-only tools to reach must also be readable inside the sandbox, or a
// command would be denied a path the read tool serves happily. Both runners
// build their read whitelist from ReadableRoots, so this covers macOS and Linux
// with one assertion; the bwrap argv is checked as well because that is the
// Linux enforcement.
func TestSessionReadGrantWidensSandbox(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GOLDER_HOME", filepath.Join(home, "ghome"))
	t.Setenv("GOLDER_SANDBOX_READABLE", "")
	t.Cleanup(func() { permissions.SetReadRoots(nil) })
	permissions.SetReadRoots(nil)

	granted := t.TempDir()
	if _, err := permissions.AddReadRoot(granted); err != nil {
		t.Fatalf("AddReadRoot: %v", err)
	}

	if roots := ReadableRoots(); !slices.Contains(roots, granted) {
		t.Errorf("a session read grant must widen the sandbox read whitelist: %v", roots)
	}

	// And the Linux runner must actually mount it read-only.
	project, tmp := t.TempDir(), t.TempDir()
	argv, _, err := (&BwrapRunner{ProjectDir: project, TmpDir: tmp}).SandboxArgv("/bin/bash", "-c", "echo hi", project)
	if err != nil {
		t.Fatalf("SandboxArgv: %v", err)
	}
	if indexOfSeq(argv, "--ro-bind", granted, granted) < 0 {
		t.Errorf("bwrap argv must read-only bind the granted directory %s: %v", granted, argv)
	}

	// Revoking it takes it back out of both, so a grant is not permanent for
	// the life of the process by accident.
	if !permissions.RemoveReadRoot(granted) {
		t.Fatal("RemoveReadRoot should report the grant was present")
	}
	if roots := ReadableRoots(); slices.Contains(roots, granted) {
		t.Errorf("a revoked grant must leave the whitelist: %v", roots)
	}
}
