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
	"slices"
	"strings"
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

// SandboxArgv implements Runner. Both directions are whitelists: a read-only
// bind is created only for the project, the system runtime and ReadableRoots,
// so the rest of $HOME (shell rc files, ~/.ssh, ~/.config/*, ~/.golder/*) is
// simply absent from the mount table and cannot be read. The project and temp
// directories are then re-bound writable, and fresh /dev (bubblewrap's
// devtmpfs also mounts devpts, so TTY sessions keep working) and /proc are
// mounted in a new pid namespace. The network is unshared (no egress) unless
// the gate granted it for this one call (CallPolicy.Network).
func (r *BwrapRunner) SandboxArgv(shell, flag, command, dir string) ([]string, func(), error) {
	return r.SandboxCall([]string{shell, flag, command}, dir, CallPolicy{})
}

// SandboxCall runs an arbitrary argv in a one-call mount and network namespace.
func (r *BwrapRunner) SandboxCall(command []string, _ string, policy CallPolicy) ([]string, func(), error) {
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
	}
	// Network is unshared by default (codex parity); --share-net restores it.
	if policy.Network {
		argv = append(argv, "--share-net")
	} else {
		argv = append(argv, "--unshare-net")
	}
	// Read-only whitelist. Directories that do not exist on this host are
	// skipped: unlike --ro-bind-try, a nonexistent source would fail the whole
	// sandbox. Home is deliberately absent unless the user added it via
	// ReadableRoots' environment/config extension.
	readRoots := append([]string{project}, ReadableRoots()...)
	readRoots = append(readRoots, WritableRoots()...)
	readRoots = append(readRoots, policy.ReadDirs...)
	readRoots = append(readRoots, policy.WriteDirs...)
	readRoots = append(readRoots, policy.ReadFiles...)
	readRoots = append(readRoots, policy.WriteFiles...)
	roots := dedupePaths(canonicalsAll(readRoots))
	// Shorter paths first so an ancestor root (e.g. an env-provided $HOME
	// entry) is mounted before a descendant file that needs its parent.
	slices.SortStableFunc(roots, func(a, b string) int { return len(a) - len(b) })
	madeDirs := map[string]bool{}
	for _, root := range roots {
		if _, err := os.Stat(root); err != nil {
			continue
		}
		// bwrap creates the final mount point but not its parents. File-level
		// whitelist entries (the git configs under $HOME) therefore need their
		// missing ancestor directories created first; --dir yields an empty
		// directory in the sandbox, so nothing is exposed by doing so.
		for _, dir := range missingAncestors(root, roots) {
			if madeDirs[dir] {
				continue
			}
			madeDirs[dir] = true
			argv = append(argv, "--dir", dir)
		}
		argv = append(argv, "--ro-bind", root, root)
	}
	argv = append(argv, "--dev", "/dev", "--proc", "/proc")
	if !policy.ReadOnlyFilesystem {
		for _, p := range canonicals(project) {
			argv = append(argv, "--bind", p, p)
		}
	}
	// Session grants from the approval dialog / config: bound writable after
	// the read-only pass above, so the writable bind wins for the same path.
	if !policy.ReadOnlyFilesystem {
		for _, root := range WritableRoots() {
			for _, p := range canonicals(root) {
				argv = append(argv, "--bind-try", p, p)
			}
		}
		for _, root := range policy.WriteDirs {
			for _, p := range canonicals(root) {
				argv = append(argv, "--bind-try", p, p)
			}
		}
		for _, file := range policy.WriteFiles {
			for _, p := range canonicals(file) {
				info, err := os.Stat(p)
				if err != nil || !info.Mode().IsRegular() {
					// bubblewrap can bind an existing file exactly, but cannot bind
					// a new file without also exposing its containing directory for
					// writing. Never widen an exact "write once" answer into a
					// directory grant: let the caller request the directory explicitly.
					return nil, nil, fmt.Errorf("bwrap: exact write to %s needs an existing regular file; grant its directory for a new file", p)
				}
				argv = append(argv, "--bind", p, p)
			}
		}
		for _, p := range canonicals(r.tmpDir()) {
			argv = append(argv, "--bind-try", p, p)
		}
	}
	// Re-assert golder's own state as read-write denied after the writable
	// binds (later mounts win), so a project rooted inside $GOLDER_HOME cannot
	// reach the trust store, credential file, or session transcripts.
	for _, p := range ProtectedGolderPaths() {
		for _, spelling := range canonicals(p) {
			info, err := os.Stat(spelling)
			if err != nil {
				continue
			}
			if info.IsDir() {
				argv = append(argv, "--tmpfs", spelling)
			} else {
				argv = append(argv, "--ro-bind", "/dev/null", spelling)
			}
		}
	}
	// Later mounts override earlier ones: mask refused children after the
	// project and granted directory binds. A directory disappears behind an
	// empty tmpfs; a file is replaced with /dev/null. For write-only refusals,
	// bind the original file read-only, preserving the ability to read it.
	for _, p := range append(append([]string{}, policy.DenyReadDirs...), policy.DenyWriteDirs...) {
		for _, spelling := range canonicals(p) {
			if info, err := os.Stat(spelling); err == nil && info.IsDir() {
				argv = append(argv, "--tmpfs", spelling)
			}
		}
	}
	for _, p := range policy.DenyReadFiles {
		for _, spelling := range canonicals(p) {
			if info, err := os.Stat(spelling); err == nil && info.Mode().IsRegular() {
				argv = append(argv, "--ro-bind", "/dev/null", spelling)
			}
		}
	}
	for _, p := range policy.DenyWriteFiles {
		for _, spelling := range canonicals(p) {
			if info, err := os.Stat(spelling); err == nil && info.Mode().IsRegular() {
				argv = append(argv, "--ro-bind", spelling, spelling)
			}
		}
	}
	// Later mounts win: re-admit the effective grants that are more specific
	// than a denied subtree. A denied directory is masked with an empty tmpfs,
	// so the child does not exist in the new namespace; --dir creates it and
	// the bind restores the grant. The combined list is used for both sides
	// because a write refusal is masked with a tmpfs as well, which also hides
	// the read side.
	deniedDirs := append(append([]string{}, policy.DenyReadDirs...), policy.DenyWriteDirs...)
	for _, root := range readGrantsInsideDenies(deniedDirs) {
		for _, p := range canonicals(root) {
			if info, err := os.Stat(p); err != nil || !info.IsDir() {
				continue
			}
			argv = append(argv, "--dir", p, "--ro-bind", p, p)
		}
	}
	for _, root := range writeGrantsInsideDenies(deniedDirs) {
		for _, p := range canonicals(root) {
			if info, err := os.Stat(p); err != nil || !info.IsDir() {
				continue
			}
			argv = append(argv, "--dir", p, "--bind", p, p)
		}
	}
	argv = append(argv, command...)
	return argv, nil, nil
}

// missingAncestors returns the parent directories of root that no other root
// already provides, root-most first. They are emitted as --dir so a file-level
// whitelist entry has somewhere to be mounted; a directory covered by another
// root is skipped because that bind is what creates it.
func missingAncestors(root string, roots []string) []string {
	covered := func(p string) bool {
		for _, r := range roots {
			if p == r || strings.HasPrefix(p, r+"/") {
				return true
			}
		}
		return false
	}
	var chain []string
	for dir := filepath.Dir(root); dir != "/" && dir != "." && dir != ""; dir = filepath.Dir(dir) {
		if covered(dir) {
			break
		}
		chain = append(chain, dir)
	}
	// root-most first
	out := make([]string, 0, len(chain))
	for i := len(chain) - 1; i >= 0; i-- {
		out = append(out, chain[i])
	}
	return out
}
