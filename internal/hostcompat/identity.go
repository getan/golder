package hostcompat

import (
	"strings"

	"github.com/getan/golder/internal/tuiosctl"
)

// Pane identity, for the processes golder spawns.
//
// A sub-agent is a golder process started by another golder process, and it
// runs as the same agent in the same pane: its reports belong on the parent's
// rail entry, not on nobody's. provider.ScrubEnv strips credential-shaped
// variables and every GOLDER_* internal from a child, which is right for
// secrets and wrong for the two things that say *which pane this is* — the
// pane's own token and the marker naming golder's workspace are caught by those
// rules.
//
// IdentityEnv is that exception, and only that exception: an explicit list of
// identity names, re-injected over the scrubbed environment. Nothing that could
// carry a general credential rides along — no provider key, no API token, no
// GOLDER_* setting — and a child that needs a provider credential still gets it
// the way it always did, from an explicit allow entry the caller builds.

// identityVars are the variables a spawned golder needs to be the pane's own
// agent. Every name is here because it names the pane or the machine it lives
// on; none of them is a secret in general use (the pane token is a proof of
// identity, good for one pane of one daemon start, and is useless as a
// credential anywhere else).
var identityVars = []string{
	// golder's own workspace.
	EnvWorkspace,
	// The pane contract golder's workspace and tuios both plant.
	tuiosctl.EnvEnv,
	tuiosctl.EnvSocket,
	tuiosctl.EnvSession,
	tuiosctl.EnvPaneID,
	tuiosctl.EnvPaneToken,
	"TUIOS_HOST",
	"TUIOS_PANE_TTY",
	// herdr.
	envHerdrEnv,
	envHerdrPaneID,
	envHerdrBinPath,
	envHerdrSocketPath,
}

// IdentityEnv returns the entries of a parent environment that say which pane
// it is, as an allow list for provider.ScrubEnv: the value of each identity
// variable that is set, and nothing else.
//
// It reads the parent list rather than the process environment so a caller can
// hand it exactly the environment it is about to derive from, and it keeps the
// first value of a name that appears twice, the way the operating system
// resolves a duplicated variable.
func IdentityEnv(parent []string) map[string]string {
	want := make(map[string]bool, len(identityVars))
	for _, name := range identityVars {
		want[name] = true
	}
	allow := make(map[string]string, len(identityVars))
	for _, entry := range parent {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || !want[name] || value == "" {
			continue
		}
		if _, seen := allow[name]; seen {
			continue
		}
		allow[name] = value
	}
	return allow
}
