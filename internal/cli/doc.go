// Package cli is the contract and shared-type layer for golder's command-line
// surface. It holds the interfaces and value types that the cmd/golder entry
// point and the internal/cli/* subpackages share, plus the assembly helpers
// those subpackages are built from (host.go, liveconfig.go, persist.go, the
// boundary path accessors).
//
// Subpackage layout:
//
//	internal/cli            — Host/Editor contracts, LiveConfig, TelemetryHolder
//	internal/cli/ui         — color, markdown, imageref helpers
//	internal/cli/config     — config-file loading and overrides
//	internal/cli/run        — run assembly and tool wiring
//	internal/cli/headless   — headless session and subagent RPC drivers
//	internal/cli/goal       — /goal state machine
//	internal/cli/btw        — /btw side thread
//	internal/cli/status     — /status command
//	internal/cli/repl       — interactive REPL and line editor
//	internal/cli/tui        — full-screen terminal UI
//	internal/cli/prompts    — slash-command registrations and their help text
//	internal/cli/sessioncmd — the session subcommand
//	internal/cli/dreamcmd   — the dream subcommand
//	internal/cli/hostcmd    — the host-compat subcommand
//	internal/cli/memstatus  — memory status helpers
//	internal/cli/testutil   — cross-subpackage test helpers
//
// The Host interface (see host.go) is the seam that lets the /goal, /btw,
// /status and REPL subpackages read and mutate the session's live state
// without importing the concrete replDeps aggregate that assembles it.
package cli
