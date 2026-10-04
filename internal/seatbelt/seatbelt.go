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
// "bwrap <policy flags> <shell> -c <command>". Both allow reads broadly,
// confine writes to the project directory and TMPDIR, and deny writes to live
// secret material (~/.ssh, ~/.gnupg) and the trust store.
package seatbelt

import (
	"os"
	"path/filepath"
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
