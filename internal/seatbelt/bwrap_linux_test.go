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

	"github.com/getan/golder/internal/grants"
	"github.com/getan/golder/internal/grants/grantstest"
	"github.com/getan/golder/internal/permissions"
)

func TestBwrapRunnerEndToEnd(t *testing.T) {
	if !Available() {
		t.Skip("bubblewrap not available or unusable on this machine")
	}
	// Put HOME outside the system-readable roots where possible. t.TempDir()
	// lands under /tmp on Linux, and /tmp is deliberately on the sandbox's
	// system read whitelist, so a .zshrc probe there would test the whitelist
	// rather than the credential masking.
	realHome, err := os.UserHomeDir()
	if err != nil {
		realHome = ""
	}
	home := ""
	if realHome != "" {
		if dir, err := os.MkdirTemp(realHome, "golder-bwrap-home-"); err == nil {
			home = dir
			defer os.RemoveAll(home)
		}
	}
	if home == "" {
		home = t.TempDir()
	}
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

	// The project is deliberately separate from $GOLDER_HOME: the state root is
	// denied wholesale, so a project rooted inside it would be entirely
	// unwritable. This layout tests the realistic case — the project is
	// writable, while the trust store next door is unreachable.
	// TmpDir is pinned to its own directory (not os.TempDir()): the "outside"
	// probe below lives under the process TMPDIR too, and binding all of it
	// would make that probe meaningless.
	project := t.TempDir()
	r := &BwrapRunner{ProjectDir: project, TmpDir: t.TempDir()}
	run := func(command string) (string, error) {
		argv, cleanup, err := r.SandboxArgv("/bin/bash", "-c", command, project)
		if err != nil {
			t.Fatalf("SandboxArgv: %v", err)
		}
		if cleanup != nil {
			defer cleanup()
		}
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir = project
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	if out, err := run("echo ok > note.txt"); err != nil {
		t.Fatalf("write inside the project must succeed: %v\n%s", err, out)
	}
	if _, err := run("cat /etc/hostname"); err != nil {
		t.Errorf("reads outside the project must stay allowed: %v", err)
	}
	// The libc resolver config must survive the mount whitelist. On
	// systemd-resolved hosts /etc/resolv.conf is a symlink into /run; if the
	// target is not bound, the link dangles and every getaddrinfo fails.
	if out, err := run("cat /etc/resolv.conf"); err != nil {
		t.Errorf("the resolver config must be readable inside the sandbox: %v\n%s", err, out)
	} else if strings.TrimSpace(out) == "" {
		t.Error("/etc/resolv.conf must not be empty inside the sandbox")
	}
	outside := t.TempDir()
	if out, err := run("echo x > " + filepath.Join(outside, "x")); err == nil {
		t.Errorf("write outside the project must fail:\n%s", out)
	}
	if out, err := run("echo hacked > " + trust); err == nil {
		t.Errorf("the trust store must stay unreachable from a sandboxed command:\n%s", out)
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
	if permissions.InsideSystemReadRoots(home) {
		t.Logf("HOME %s is inside a system-readable root; skipping the .zshrc denial probe", home)
	} else {
		secret := filepath.Join(home, ".zshrc")
		if err := os.WriteFile(secret, []byte("export API_KEY=leaked"), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := run("cat " + secret); err == nil {
			t.Errorf("reading $HOME/.zshrc must be denied inside the sandbox:\n%s", out)
		} else if strings.Contains(out, "leaked") {
			t.Errorf("shell rc content leaked out of the sandbox:\n%s", out)
		}
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

	// A session read grant must reach the sandbox as well: the read-only tools
	// can read a granted directory (that is what the approval dialog promised),
	// so a command must not be denied the same path. This is the end-to-end
	// proof for the Linux runner that the argv-level test asserts by reading.
	granted := t.TempDir()
	grantedFile := filepath.Join(granted, "granted.txt")
	if err := os.WriteFile(grantedFile, []byte("granted content"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The fresh table first, then the grant: Install replaces the process table
	// (and clears it at test end), so calling it after the Add would wipe the
	// row this test exists to prove — the sandbox would then be asked about a
	// directory nobody granted, and the read below would fail for the wrong
	// reason.
	grantstest.Install(t)
	if _, err := grants.AddReadRoot(granted, grants.Session, "test"); err != nil {
		t.Fatalf("AddReadRoot: %v", err)
	}
	if out, err := run("cat " + grantedFile); err != nil {
		t.Errorf("a granted directory must be readable inside the sandbox: %v\n%s", err, out)
	} else if !strings.Contains(out, "granted content") {
		t.Errorf("granted read returned unexpected content:\n%s", out)
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

// TestBwrapArgvBindsResolverConfigByFile guards the DNS fix at the argv level:
// the resolved resolver config may be bound as an exact file, but its directory
// must never be. /run/systemd/resolve also contains the world-accessible
// io.systemd.Resolve control sockets, so a directory bind would hand the sandbox
// the host resolver's control interface.
func TestBwrapArgvBindsResolverConfigByFile(t *testing.T) {
	grantstest.Install(t)
	t.Setenv("GOLDER_SANDBOX_READABLE", "")
	t.Setenv("GOLDER_HOME", filepath.Join(t.TempDir(), "ghome"))
	project := t.TempDir()
	argv, _, err := (&BwrapRunner{ProjectDir: project, TmpDir: t.TempDir()}).SandboxArgv("/bin/bash", "-c", "true", project)
	if err != nil {
		t.Fatalf("SandboxArgv: %v", err)
	}
	for _, dir := range []string{"/run", "/run/systemd", "/run/systemd/resolve"} {
		for _, flag := range []string{"--ro-bind", "--bind", "--bind-try"} {
			if indexOfSeq(argv, flag, dir, dir) >= 0 {
				t.Errorf("%s must never bind the resolver directory %s: %v", flag, dir, argv)
			}
		}
	}
	// Positive half: when /etc/resolv.conf is a symlink to a regular file, the
	// argv must carry an exact read-only bind for the target, not just its
	// ancestors.
	target, err := filepath.EvalSymlinks("/etc/resolv.conf")
	if err != nil || target == "/etc/resolv.conf" {
		return
	}
	if info, err := os.Stat(target); err != nil || !info.Mode().IsRegular() {
		return
	}
	if indexOfSeq(argv, "--ro-bind", target, target) < 0 {
		t.Errorf("argv must bind the resolved resolver config %s: %v", target, argv)
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
