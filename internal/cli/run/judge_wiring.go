package run

import (
	"github.com/smallnest/pigo/internal/agentcore"
	"github.com/smallnest/pigo/internal/agenttool"
	"github.com/smallnest/pigo/internal/judge"
	"github.com/smallnest/pigo/internal/seatbelt"
)

// WireBashSandbox injects the risk grader and the seatbelt runner into every
// BashTool in tools. It is called from BuiltinTools so every driver (REPL,
// TUI, headless, /btw side runs, task children, sub-agent RPC, webhook)
// inherits the same execution-layer grading without its own wiring.
//
// The matrix is deliberately conservative:
//
//   - PIGO_JUDGE=off → nothing is set; execution is exactly today's.
//   - No TYPESAFE_API_KEY → no grader (Judge stays nil): zero-config runs
//     keep today's behavior plus the static hard-deny floor enforced by the
//     BeforeToolCall gate.
//   - PIGO_SANDBOX=off → grader only, never isolate.
//   - PIGO_SANDBOX=auto (default) → sandbox-tier verdicts run under
//     sandbox-exec when a runner is available (macOS); without one they run
//     directly (the gate already prompted for them).
//   - PIGO_SANDBOX=enforce → every foreground command runs sandboxed and
//     fails closed when no runner is available.
func WireBashSandbox(tools []agentcore.AgentTool, cwd string) {
	if !judge.GateEnabled() {
		return
	}
	mode := seatbelt.ModeFromEnv()
	configured := judge.GraderConfigured()
	if mode == seatbelt.ModeOff && !configured {
		return
	}
	trusted := Trusted(cwd)
	for _, t := range tools {
		b, ok := t.(*agenttool.BashTool)
		if !ok {
			continue
		}
		if configured {
			b.Judge = judge.ClassifierForCwd(cwd, trusted)
		}
		switch mode {
		case seatbelt.ModeEnforce:
			b.ForceSandbox = true
			b.Sandbox = seatbelt.New(cwd)
		case seatbelt.ModeAuto:
			if configured {
				b.Sandbox = seatbelt.New(cwd)
			}
		}
	}
}
