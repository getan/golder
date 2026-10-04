// Package seatbelt confines bash execution with the OS sandbox: macOS
// sandbox-exec, Linux bubblewrap (bwrap); platforms without a runner compile
// to an unavailable stub. It is a leaf package: standard library only, no
// imports from agenttool/judge, so the cli layer can inject it into the bash
// tool without creating an import cycle (agenttool defines its own
// SandboxRunner interface; the platform runners satisfy it structurally).
//
// The runner only changes the execution layer: the tool name, arguments,
// and CLI surface are untouched. On macOS a sandboxed command runs as
// "sandbox-exec -f <generated profile> <shell> -c <command>"; on Linux as
// "bwrap <policy flags> <shell> -c <command>". Both policies are whitelists:
// reads are allowed only for the project, the system runtime and the extra
// roots in ReadableRoots (so everything else under $HOME is unreadable by
// construction), writes are confined to the project and TMPDIR, and the
// network is off unless GOLDER_SANDBOX_NETWORK opts in.
package seatbelt

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Mode selects when bash runs under the sandbox.
type Mode int

const (
	// ModeOff never sandboxes; verdicts run directly.
	ModeOff Mode = iota
	// ModeAuto (default) sandboxes only sandbox-tier verdicts.
	ModeAuto
	// ModeEnforce runs every foreground bash command under the sandbox and
	// fails closed when no runner is available.
	ModeEnforce
)

// ModeFromEnv resolves GOLDER_SANDBOX (off|auto|enforce, default auto).
func ModeFromEnv() Mode {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GOLDER_SANDBOX"))) {
	case "off":
		return ModeOff
	case "enforce":
		return ModeEnforce
	default:
		return ModeAuto
	}
}

// NetworkEnabled reports whether sandboxed commands keep network access.
// Off by default (codex parity: egress must be granted explicitly, so a
// sandboxed command cannot exfiltrate what it reads). Set
// GOLDER_SANDBOX_NETWORK=on (also 1/true/allow/yes) to re-enable it for
// toolchains that legitimately need the network inside the sandbox.
func NetworkEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GOLDER_SANDBOX_NETWORK"))) {
	case "on", "1", "true", "allow", "yes":
		return true
	default:
		return false
	}
}

// ReadableRoots returns the directories the sandbox allows READING, besides the
// project itself (which both runners always add). This is a whitelist: anything
// not listed is unreadable inside the sandbox, so credential material under
// $HOME — shell rc files, SSH/GPG keys, ~/.config/*, ~/.golder/* — is excluded
// by construction rather than by an ever-growing blacklist.
//
// The list covers what a toolchain actually needs: the system runtime (exec,
// dylibs, /usr, /bin), the package-manager trees Homebrew/Linuxbrew install
// into, and platform metadata directories. $HOME is deliberately NOT here; a
// user whose toolchain lives in $HOME (conda, pyenv, sdkman, nvm) adds it via
// the [sandbox] readable config (see ExtraReadableRoots).
//
// GOLDER_SANDBOX_READABLE extends the list from the environment (colon- or
// comma-separated absolute paths).
func ReadableRoots() []string {
	roots := []string{
		// System runtime and libraries: exec, dyld, homebrew.
		"/usr", "/bin", "/sbin", "/lib", "/lib64", "/opt", "/etc",
	}
	if runtime.GOOS == "darwin" {
		roots = append(roots,
			"/System", "/Library", "/Applications",
			"/private/var/db", "/private/etc", "/private/tmp", "/private/var/tmp",
		)
	} else {
		roots = append(roots, "/var/lib", "/var/cache")
	}
	// Device nodes and the temp roots a shell needs; the runners handle their
	// own /dev and /proc mounts, these are for any path-based checks.
	roots = append(roots, "/dev", "/proc")
	roots = append(roots, readableGitFiles()...)
	roots = append(roots, sandboxReadableFromEnv()...)
	return roots
}

// readableGitFiles returns the git configuration files a sandboxed command
// legitimately needs, so `git commit` can find its identity and `git status`
// honors the global ignore list. Only these two files are exposed — not the
// containing directories.
//
// Caveat: ~/.gitconfig CAN carry credentials (url.<base>.insteadOf entries
// with embedded tokens, http.extraHeader authorization, or a credential
// helper that reads a secret). Users who keep secrets there should drop the
// git files from the read whitelist by setting GOLDER_SANDBOX_READABLE to a
// value and accepting that `git commit` then needs --author/GIT_AUTHOR_*
// env vars, or run the commit outside the sandbox.
func readableGitFiles() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	out := []string{filepath.Join(home, ".gitconfig")}
	xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if xdg == "" {
		xdg = filepath.Join(home, ".config")
	}
	if filepath.IsAbs(xdg) {
		out = append(out,
			filepath.Join(xdg, "git", "ignore"),
			filepath.Join(xdg, "git", "attributes"),
		)
	}
	return out
}

// ProtectedGolderPaths returns golder's own security-critical state. It is
// normally outside the read whitelist anyway (it lives in $GOLDER_HOME), but a
// project rooted inside that directory would otherwise inherit writable access
// through the project bind — so both runners re-assert these as read-write
// denied AFTER their allow rules. Reading is denied too: the credential file
// holds live API keys and trust.json decides what the agent may run.
func ProtectedGolderPaths() []string {
	home, err := os.UserHomeDir()
	golderHome := strings.TrimSpace(os.Getenv("GOLDER_HOME"))
	if golderHome == "" {
		if err != nil || home == "" {
			return nil
		}
		golderHome = filepath.Join(home, ".golder")
	}
	return []string{
		filepath.Join(golderHome, "trust.json"),
		filepath.Join(golderHome, ".credentials.yaml"),
		filepath.Join(golderHome, "sessions"),
	}
}

// sandboxReadableFromEnv parses GOLDER_SANDBOX_READABLE (colon- or
// comma-separated), keeping only absolute paths so a relative entry cannot
// silently widen the sandbox relative to the launch directory.
func sandboxReadableFromEnv() []string {
	raw := strings.TrimSpace(os.Getenv("GOLDER_SANDBOX_READABLE"))
	if raw == "" {
		return nil
	}
	var out []string
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ':' || r == ',' }) {
		part = strings.TrimSpace(part)
		if filepath.IsAbs(part) {
			out = append(out, part)
		}
	}
	return out
}

// Runner builds a sandboxed argv for one shell invocation. dir is advisory
// (the caller keeps cmd.Dir); cleanup removes the generated profile and
// runs after the command finishes.
type Runner interface {
	SandboxArgv(shell, flag, command, dir string) (argv []string, cleanup func(), err error)
}

// canonicals returns the path plus its symlink-resolved form (deduped). Both
// runners carry both forms: macOS sandbox profiles match on the canonical
// vnode path (/var is a symlink to /private/var there), and bubblewrap mounts
// bind by path, so enumerating both keeps the allowed-write set correct
// regardless of which form the caller used.
func canonicals(p string) []string {
	if p == "" {
		return nil
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil || real == p {
		return []string{p}
	}
	return []string{p, real}
}

// canonicalsAll expands every path to its symlink spellings (see canonicals).
func canonicalsAll(paths []string) []string {
	var out []string
	for _, p := range paths {
		out = append(out, canonicals(p)...)
	}
	return out
}

// dedupePaths removes duplicate spellings (canonicals can repeat a path when it
// is already canonical), preserving order so mount and profile lists stay
// stable.
func dedupePaths(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}
