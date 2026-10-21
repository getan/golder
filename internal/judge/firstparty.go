package judge

// FirstParty is the fixed classifier for golder's own narrow tools. It exists
// because these tools have a closed schema and a known meaning: a pane read is
// a read, and a pane command is a shell command. It is not an annotation a
// plugin can claim, and it is not a general "trust this tool name" switch.

import (
	"context"
	"encoding/json"
	"strings"
)

// FirstParty runs before the JEV/local routine grader. Tools it does not know
// fall through unchanged; tools it knows return a verdict with a source.
type FirstParty struct {
	Next Classifier
}

func (f FirstParty) Classify(ctx context.Context, tool string, args json.RawMessage) Verdict {
	switch tool {
	case "tuios_list_windows", "tuios_capture_pane", "tuios_wait_for":
		return Verdict{
			Level: Allow, Source: LocalSource, Confidence: 1, Risk: "low",
			Reasons: []string{"tuios read-only tool"},
		}
	case "tuios_run":
		var a struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(args, &a); err != nil || strings.TrimSpace(a.Command) == "" {
			return f.failClosed("tuios_run has no command")
		}
		return f.classifyShell(ctx, a.Command)
	case "tuios_window":
		var a struct {
			Action  string   `json:"action"`
			Command []string `json:"command"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return f.failClosed("tuios_window has no action")
		}
		switch a.Action {
		case "new":
			if len(a.Command) == 0 {
				return Verdict{Level: Allow, Source: LocalSource, Confidence: 1, Risk: "low",
					Reasons: []string{"opening a shell pane"}}
			}
			return f.classifyShell(ctx, strings.Join(a.Command, " "))
		case "focus":
			return Verdict{Level: Confirm, Source: LocalSource, Confidence: 1, Risk: "low",
				Reasons: []string{"focusing an existing pane"}}
		case "close":
			// Closing a pane can kill a running task; leave that to the
			// contextual reviewer rather than a fixed local allow.
			return f.next(ctx, tool, args)
		}
		return f.failClosed("unknown tuios_window action")
	}
	return f.next(ctx, tool, args)
}

func (f FirstParty) classifyShell(ctx context.Context, command string) Verdict {
	raw, _ := json.Marshal(map[string]string{"command": command})
	return f.next(ctx, "bash", raw)
}

func (f FirstParty) next(ctx context.Context, tool string, args json.RawMessage) Verdict {
	if f.Next == nil {
		return f.failClosed("no reviewer behind the first-party classifier")
	}
	return f.Next.Classify(ctx, tool, args)
}

func (f FirstParty) failClosed(reason string) Verdict {
	return Verdict{Level: Confirm, Failed: true, Source: "static", Reasons: []string{reason}}
}
