//go:build darwin

package seatbelt

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runSandboxed executes one shell command under the runner's profile and
// returns its combined output. It fails the test if the profile cannot be
// generated; the caller decides whether the command itself must succeed.
func runSandboxed(t *testing.T, r Runner, dir, command string) (string, error) {
	t.Helper()
	argv, cleanup, err := r.SandboxArgv("/bin/bash", "-c", command, dir)
	if err != nil {
		t.Fatalf("SandboxArgv: %v", err)
	}
	defer cleanup()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func requireSandboxExec(t *testing.T) {
	t.Helper()
	if !Available() {
		t.Skip("sandbox-exec not on PATH")
	}
}

// TestSeatbeltEnforcesProjectBoundary proves the profile actually isolates:
// writes inside the project (and the sandbox tmp dir) succeed, while writes
// anywhere else fail. The existing content test only greps the profile text;
// this runs commands through it, so a rule-ordering or quoting regression
// that still "contains" the right strings is caught here.
func TestSeatbeltEnforcesProjectBoundary(t *testing.T) {
	requireSandboxExec(t)
	project := t.TempDir()
	sandboxTmp := t.TempDir()
	outside := t.TempDir()
	r := &SeatbeltRunner{ProjectDir: project, TmpDir: sandboxTmp}

	// Control: a plain read works under the sandbox.
	if out, err := runSandboxed(t, r, project, "cat /etc/hosts"); err != nil {
		t.Fatalf("read under sandbox failed: %v\n%s", err, out)
	}

	// Write inside the project succeeds.
	if out, err := runSandboxed(t, r, project, "echo inside > hello.txt && cat hello.txt"); err != nil {
		t.Fatalf("write inside project failed: %v\n%s", err, out)
	} else if strings.TrimSpace(out) != "inside" {
		t.Fatalf("readback = %q, want %q", out, "inside")
	}

	// Write to the sandbox tmp dir succeeds (toolchains spill there).
	tmpFile := filepath.Join(sandboxTmp, "spill.txt")
	if out, err := runSandboxed(t, r, project, fmt.Sprintf("echo tmp > %q && cat %q", tmpFile, tmpFile)); err != nil {
		t.Fatalf("write to sandbox tmp dir failed: %v\n%s", err, out)
	}

	// Write outside both trees fails and leaves nothing behind.
	evil := filepath.Join(outside, "evil.txt")
	if out, err := runSandboxed(t, r, project, fmt.Sprintf("echo pwn > %q", evil)); err == nil {
		_ = os.Remove(evil)
		t.Fatalf("write outside project succeeded, sandbox did not contain it (output %q)", out)
	}
	if _, statErr := os.Stat(evil); !os.IsNotExist(statErr) {
		_ = os.Remove(evil)
		t.Fatal("outside file exists after a denied write")
	}
}

// TestSeatbeltDeniesSecretsAndTrustStore proves the deny rules override the
// broad project/tmp allows: live secret directories and the trust store are
// never writable, even when they sit inside an otherwise writable tree.
// Each probe uses a unique name and asserts nothing was created, so a
// regression fails the test instead of littering the host.
func TestSeatbeltDeniesSecretsAndTrustStore(t *testing.T) {
	requireSandboxExec(t)
	project := t.TempDir()
	r := &SeatbeltRunner{ProjectDir: project, TmpDir: t.TempDir()}

	// Control: ordinary project writes work, so a denial below is the deny
	// rule firing, not a generally broken shell.
	if out, err := runSandboxed(t, r, project, "echo ok > notes.txt"); err != nil {
		t.Fatalf("control write failed: %v\n%s", err, out)
	}

	// trust.json inside the project is denied even though the project tree
	// is writable (the deny sits after the allow in the profile).
	trust := filepath.Join(project, "trust.json")
	if out, err := runSandboxed(t, r, project, "echo x > trust.json"); err == nil {
		_ = os.Remove(trust)
		t.Fatalf("write to trust.json succeeded, deny rule did not fire (output %q)", out)
	}
	if _, statErr := os.Stat(trust); !os.IsNotExist(statErr) {
		_ = os.Remove(trust)
		t.Fatal("trust.json exists after a denied write")
	}

	// A similarly named backup is still writable: the deny is anchored to
	// the trust.json basename, not a prefix.
	if out, err := runSandboxed(t, r, project, "echo x > trust.json.bak && cat trust.json.bak"); err != nil {
		t.Fatalf("trust.json.bak should stay writable: %v\n%s", err, out)
	}

	// Live secret directories are denied. Probe names are unique per run;
	// the test asserts the probe was never created.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	suffix := fmt.Sprintf("golder-sandbox-probe-%d-%d", os.Getpid(), time.Now().UnixNano())
	for _, dir := range []string{filepath.Join(home, ".ssh"), filepath.Join(home, ".gnupg")} {
		if st, statErr := os.Stat(dir); statErr != nil || !st.IsDir() {
			t.Logf("no %s dir, skipping its probe", dir)
			continue
		}
		// Reading is denied too: a probe file created on the host must not be
		// readable inside the sandbox.
		hostProbe := filepath.Join(dir, suffix+".read")
		if writeErr := os.WriteFile(hostProbe, []byte("secret-material"), 0o600); writeErr == nil {
			out, runErr := runSandboxed(t, r, project, fmt.Sprintf("cat %q", hostProbe))
			_ = os.Remove(hostProbe)
			if runErr == nil && strings.Contains(out, "secret-material") {
				t.Fatalf("reading %s succeeded, read-deny did not fire (output %q)", dir, out)
			}
		} else {
			t.Logf("could not create read probe in %s: %v", dir, writeErr)
		}
		probe := filepath.Join(dir, suffix)
		out, runErr := runSandboxed(t, r, project, fmt.Sprintf("touch %q", probe))
		if runErr == nil {
			_ = os.Remove(probe)
			t.Fatalf("write to %s succeeded, deny rule did not fire (output %q)", dir, out)
		}
		if _, statErr := os.Stat(probe); !os.IsNotExist(statErr) {
			_ = os.Remove(probe)
			t.Fatalf("probe %q exists after a denied write", probe)
		}
	}
}

// TestSeatbeltAllowsWritesThroughSymlinkedRoot is the regression for the
// TestSeatbeltNetworkOff proves the default profile has no egress: an
// outbound connection fails inside the sandbox. GOLDER_SANDBOX_NETWORK=on is
// covered by the profile-text test (it must emit "(allow network*)").
func TestSeatbeltNetworkOff(t *testing.T) {
	requireSandboxExec(t)
	t.Setenv("GOLDER_SANDBOX_NETWORK", "")
	project := t.TempDir()
	r := &SeatbeltRunner{ProjectDir: project, TmpDir: t.TempDir()}
	out, err := runSandboxed(t, r, project, "exec 3<>/dev/tcp/1.1.1.1/80 && echo CONNECTED || echo BLOCKED")
	if err != nil {
		t.Fatalf("probe command failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "BLOCKED") {
		t.Errorf("network should be off inside the sandbox, got:\n%s", out)
	}
}

// TestSeatbeltAllowsWritesThroughSymlinkedRoot is the regression for the
// silent-deny bug: on macOS the temp tree lives under /var, a symlink to
// /private/var, while sandbox matches canonical vnode paths. A profile that
// carries only the symlinked form denies every write it meant to allow, and
// the failure looks like a broken command rather than a sandbox rule. The
// profile must carry both forms.
func TestSeatbeltAllowsWritesThroughSymlinkedRoot(t *testing.T) {
	requireSandboxExec(t)
	// t.TempDir() on macOS resolves to /var/folders/… — already the symlinked
	// form, which is exactly the case that used to fail.
	project := t.TempDir()
	sandboxTmp := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(project); err == nil && resolved == project {
		t.Skipf("%s has no symlinked form to exercise", project)
	}

	r := &SeatbeltRunner{ProjectDir: project, TmpDir: sandboxTmp}
	if out, err := runSandboxed(t, r, project, "echo hello > through-symlink.txt && cat through-symlink.txt"); err != nil {
		t.Fatalf("write through the symlinked project root failed: %v\n%s", err, out)
	} else if strings.TrimSpace(out) != "hello" {
		t.Fatalf("readback = %q, want %q", out, "hello")
	}

	// Same for the temp dir the profile allows (toolchains spill there).
	probe := filepath.Join(sandboxTmp, "spill.txt")
	if out, err := runSandboxed(t, r, project, fmt.Sprintf("echo t > %q", probe)); err != nil {
		t.Fatalf("write through the symlinked tmp dir failed: %v\n%s", err, out)
	}
}

// TestCanonicalsCoversBothForms locks the helper the profile depends on: a
// path under a symlink yields both spellings (caller's first), a path with no
// symlinked form yields exactly one entry, and "" yields nothing.
func TestCanonicalsCoversBothForms(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	got := canonicals(link)
	if len(got) != 2 {
		t.Fatalf("canonicals(%q) = %v, want both the link and its target", link, got)
	}
	if got[0] != link {
		t.Errorf("canonicals should keep the caller's spelling first, got %v", got)
	}
	if resolved, err := filepath.EvalSymlinks(link); err == nil && got[1] != resolved {
		t.Errorf("second entry = %q, want the resolved %q", got[1], resolved)
	}

	// A directory with no symlinked form anywhere in its path yields one
	// entry. On macOS that means a path outside /var, /tmp, and /etc (all of
	// which are symlinks into /private); fall back to asserting the dedup
	// rule when the platform offers no such root.
	plain := filepath.Join(string(filepath.Separator), "usr")
	if got := canonicals(plain); len(got) != 1 || got[0] != plain {
		if resolved, err := filepath.EvalSymlinks(plain); err == nil && resolved != plain {
			t.Logf("%s is itself a symlink; skipping the single-entry assertion", plain)
		} else {
			t.Errorf("canonicals(%q) = %v, want just the path", plain, got)
		}
	}
	if got := canonicals(""); got != nil {
		t.Errorf("canonicals(\"\") = %v, want nil", got)
	}
}
