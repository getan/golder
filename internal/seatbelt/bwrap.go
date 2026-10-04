package seatbelt

// This file implements the Linux sandbox runner over bubblewrap (bwrap). It
// mirrors the macOS seatbelt policy: reads stay broad, writes are confined to
// the project directory and TMPDIR, live secret material (SSH/GPG keys, the
// trust store, the credential file, stored sessions) is forced read-only even
// when an enclosing writable bind would otherwise cover it, and the network
// stays open — exfiltration is the judge's job, not the filesystem profile's.
//
// bubblewrap takes its policy as flags rather than a profile file, so the
// runner builds an argv only and the cleanup function is nil. It lives in a
// file without a build tag so the argument construction is unit-testable on
// any platform; seatbelt_linux.go wires it to Available/New.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// bwrapAvailable probes that bubblewrap is installed AND can actually create a
// sandbox here — unprivileged user namespaces enabled and not blocked by an
// LSM policy. The probe runs once per process: on hardened distros bwrap can
// be present but unusable, and detecting that at wiring time surfaces as the
// documented fallback (prompt, or fail closed under enforce) instead of a
// confusing per-command failure.
var bwrapAvailable = sync.OnceValue(func() bool {
	path, err := exec.LookPath("bwrap")
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The same mount/namespace shape the runner uses, against `true`, so the
	// probe exercises exact paths (ro-bind root, dev, proc, pid namespace).
	cmd := exec.CommandContext(ctx, path,
		"--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc",
		"--unshare-pid", "--die-with-parent", "true")
	return cmd.Run() == nil
})

// BwrapRunner builds a sandboxed argv for one shell invocation under
// bubblewrap. ProjectDir should be the run's working directory; TmpDir
// defaults to os.TempDir when empty.
type BwrapRunner struct {
	ProjectDir string
	TmpDir     string
}

func (r *BwrapRunner) tmpDir() string {
	if r.TmpDir != "" {
		return r.TmpDir
	}
	return os.TempDir()
}

// secretPaths returns the live secret material the sandbox must keep
// read-only even when it falls inside a writable bind (a project rooted at
// $HOME, or at $GOLDER_HOME). It re-derives the locations instead of importing
// internal/trust or internal/provider so this package stays a stdlib-only
// leaf; the paths are stable, documented conventions (see the README table).
func secretPaths() []string {
	var out []string
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		out = append(out, filepath.Join(home, ".ssh"), filepath.Join(home, ".gnupg"))
	}
	golderHome := os.Getenv("GOLDER_HOME")
	if golderHome == "" && home != "" {
		golderHome = filepath.Join(home, ".golder")
	}
	if golderHome != "" {
		out = append(out,
			filepath.Join(golderHome, "trust.json"),
			filepath.Join(golderHome, ".credentials.yaml"),
			filepath.Join(golderHome, "sessions"),
		)
	}
	return out
}

// SandboxArgv implements Runner. The resulting argv mounts a read-only view of
// the host root, fresh /dev (bubblewrap's devtmpfs also mounts devpts, so TTY
// sessions keep working) and /proc in a new pid namespace, then overrides the
// project and temp directories as writable. Secret paths are re-asserted
// read-only AFTER those binds, because later mounts win: without the ordering
// a project rooted at $HOME would expose ~/.ssh and the trust store to writes.
func (r *BwrapRunner) SandboxArgv(shell, flag, command, dir string) ([]string, func(), error) {
	project := r.ProjectDir
	if project == "" {
		var err error
		project, err = os.Getwd()
		if err != nil {
			return nil, nil, fmt.Errorf("bwrap: resolve project dir: %w", err)
		}
	}
	argv := []string{
		"bwrap",
		"--die-with-parent",
		"--unshare-pid", "--unshare-uts", "--unshare-ipc",
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
	}
	for _, p := range canonicals(project) {
		argv = append(argv, "--bind", p, p)
	}
	for _, p := range canonicals(r.tmpDir()) {
		argv = append(argv, "--bind-try", p, p)
	}
	for _, p := range secretPaths() {
		if p != "" {
			argv = append(argv, "--ro-bind-try", p, p)
		}
	}
	argv = append(argv, shell, flag, command)
	return argv, nil, nil
}
