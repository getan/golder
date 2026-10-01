// Package seatbelt confines bash execution with the OS sandbox (macOS
// sandbox-exec; other platforms compile to an unavailable stub). It is a
// leaf package: standard library only, no imports from agenttool/judge, so
// the cli layer can inject it into the bash tool without creating an import
// cycle (agenttool defines its own SandboxRunner interface; SeatbeltRunner
// satisfies it structurally).
//
// The runner only changes the execution layer: the tool name, arguments,
// and CLI surface are untouched. A sandboxed command runs as
// "sandbox-exec -f <generated profile> <shell> -c <command>" with the same
// working directory. The generated profile allows reads broadly, confines
// writes to the project directory and TMPDIR, and denies writes to live
// secret material (~/.ssh, ~/.gnupg) and the trust store.
package seatbelt

import (
	"os"
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

// ModeFromEnv resolves PIGO_SANDBOX (off|auto|enforce, default auto).
func ModeFromEnv() Mode {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("PIGO_SANDBOX"))) {
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
