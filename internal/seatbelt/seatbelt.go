// Package seatbelt confines bash execution with the OS sandbox: macOS
// sandbox-exec, Linux bubblewrap (bwrap); platforms without a runner compile
// to an unavailable stub. It is a leaf package: standard library only, no
// imports from agenttool/judge, so the cli layer can inject it into the bash
// tool without creating an import cycle (agenttool defines its own
// SandboxRunner interface; the platform runners satisfy it structurally).
//
// The runner only changes the execution layer: the tool name, arguments, and
// CLI surface are untouched. On macOS a sandboxed call runs as "sandbox-exec -f
// <generated profile> <argv…>"; on Linux as "bwrap <policy flags> <argv…>".
// Both policies are whitelists: reads are allowed only for the project, the
// system runtime and the extra roots in ReadableRoots (so everything else under
// $HOME is unreadable by construction), and writes are confined to the project,
// TMPDIR and the session's writable roots.
//
// Isolation is the DEFAULT for every approved bash call and for the native file
// tools (which run through a one-call worker). Network and extra paths are
// per-call capabilities in CallPolicy, decided by the authorization gate for
// that single invocation — a tool argument can never create one.
package seatbelt

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/getan/golder/internal/golderhome"
	"github.com/getan/golder/internal/grants"
	"github.com/getan/golder/internal/permissions"
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
	// The system halves come from permissions.SystemReadRoots, so the sandbox
	// and the gates that refuse to ask about a sandbox-admissible path share
	// one definition of "always readable".
	roots := permissions.SystemReadRoots()
	roots = append(roots, readableGitFiles()...)
	// Session read grants: directories the user allowed the read-only tools to
	// reach outside the workspace. They come from the authorization table,
	// which is the one store every layer reads, so a grant the boundary gate
	// honours is a grant the sandbox admits — a grant the gate honours but the
	// profile refuses would fail the moment a command touched it, which is
	// worse than not offering it.
	roots = append(roots, grants.ReadRoots()...)
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

// ProtectedGolderPaths returns golder's own state, denied read AND write. It is
// normally outside the read whitelist anyway (it lives in the state root), but a
// project rooted inside that directory would otherwise inherit writable access
// through the project bind — so both runners re-assert this AFTER their allow
// rules.
//
// The whole root is denied, not a list of files inside it. A list was the old
// shape and it had already fallen behind: grants.json, the one file that decides
// what the agent may read and write, sat outside it while trust.json sat inside.
// Denying the directory means a file added to the state root later is protected
// by construction, and reading is denied too — the credential file holds live
// API keys, and the authorization table is what the gates enforce.
func ProtectedGolderPaths() []string {
	if dir := golderhome.Dir(); dir != "" {
		return []string{dir}
	}
	return nil
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
	CallRunner
}

// CallPolicy is an execution capability, scoped to exactly one sandboxed call.
// It is constructed after the gates have decided; a tool argument cannot create
// one. The sandbox may open the network without opening any additional files.
// ReadFiles and WriteFiles are one-call, exact-file grants, not directory roots.
type CallPolicy struct {
	Network bool
	// OwnTerminal authorizes normal terminal operations only on the child's
	// allocated PTY. Darwin binds the device after allocation, before exec;
	// Linux uses bubblewrap's private /dev/devpts. It is never a caller path.
	OwnTerminal bool
	// ExtensionState is minted by the extension manager from an operator source
	// declaration. Unlike ReadDirs/WriteDirs it can expose one protected HOME.
	ExtensionState *ExtensionState
	// ReadOnlyFilesystem is for native read/search workers. They may create no
	// files, including inside the workspace and temp directory. Bash and patch
	// workers leave this false: their writes remain confined to permitted roots.
	ReadOnlyFilesystem bool
	ReadDirs           []string
	WriteDirs          []string
	ReadFiles          []string
	WriteFiles         []string
	// Explicit refusals override a broader allow at the OS boundary too.
	DenyReadDirs   []string
	DenyWriteDirs  []string
	DenyReadFiles  []string
	DenyWriteFiles []string
}

// RefusedPaths mirrors effective grant-table refusals into one OS sandbox
// policy. A denied child must stay denied even when an ancestor was mounted by
// an earlier directory grant. The OS layer is the last line of defence for a
// shell program or native worker that does not use the path helper correctly.
func RefusedPaths(policy *CallPolicy) {
	if policy == nil {
		return
	}
	s := grants.Process()
	if s == nil {
		return
	}
	for _, g := range s.Entries().All() {
		if g.Deny == 0 {
			continue
		}
		readDenied := g.Deny.Has(grants.Read) && s.Check(g.Path, grants.Read).Decision == grants.Denied
		writeDenied := g.Deny.Has(grants.Write) && s.Check(g.Path, grants.Write).Decision == grants.Denied
		if g.Scope == grants.Exact {
			if readDenied {
				policy.DenyReadFiles = append(policy.DenyReadFiles, g.Path)
			}
			if writeDenied {
				policy.DenyWriteFiles = append(policy.DenyWriteFiles, g.Path)
			}
		} else {
			if readDenied {
				policy.DenyReadDirs = append(policy.DenyReadDirs, g.Path)
			}
			if writeDenied {
				policy.DenyWriteDirs = append(policy.DenyWriteDirs, g.Path)
			}
		}
	}
}

// CallRunner accepts per-call capabilities and an argv, so the same OS policy
// can isolate a shell and a one-shot native-tool worker. A nil policy is the
// default: no network and no additional paths.
type CallRunner interface {
	SandboxCall(argv []string, dir string, policy CallPolicy) (sandboxed []string, cleanup func(), err error)
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
