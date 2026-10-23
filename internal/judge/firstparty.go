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
	case "tuios":
		var a struct {
			Action  string   `json:"action"`
			Command string   `json:"command"`
			Text    string   `json:"text"`
			Submit  bool     `json:"submit"`
			Argv    []string `json:"argv"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return f.failClosed("tuios has no action")
		}
		switch a.Action {
		case "list", "capture", "wait":
			return Verdict{
				Level: Allow, Source: LocalSource, Confidence: 1, Risk: "low",
				Reasons: []string{"tuios read-only action"},
			}
		case "run":
			if strings.TrimSpace(a.Command) == "" {
				return f.failClosed("tuios run has no command")
			}
			return f.classifyShell(ctx, a.Command)
		case "send":
			if strings.TrimSpace(a.Text) == "" && !a.Submit {
				return f.failClosed("tuios send has no text")
			}
			return f.next(ctx, tool, args)
		case "new":
			if len(a.Argv) == 0 {
				return Verdict{Level: Allow, Source: LocalSource, Confidence: 1, Risk: "low",
					Reasons: []string{"opening a shell pane"}}
			}
			return f.classifyShell(ctx, strings.Join(a.Argv, " "))
		case "focus":
			return Verdict{Level: Confirm, Source: LocalSource, Confidence: 1, Risk: "low",
				Reasons: []string{"focusing an existing pane"}}
		case "move", "resize", "rename", "minimize":
			return Verdict{Level: Confirm, Source: LocalSource, Confidence: 1, Risk: "low",
				Reasons: []string{"changing an existing pane"}}
		case "close":
			// Closing a pane can kill a running task; leave that to the
			// contextual reviewer rather than a fixed local allow.
			return f.next(ctx, tool, args)
		}
		return f.failClosed("unknown tuios action")
	case "browser":
		var a struct {
			Action string `json:"action"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return f.failClosed("browser has no action")
		}
		switch a.Action {
		case "list", "snapshot", "find", "get", "is", "wait", "dialog",
			"console", "errors", "network":
			// Observing a page changes nothing on the host or on the page.
			return Verdict{
				Level: Allow, Source: LocalSource, Confidence: 1, Risk: "low",
				Reasons: []string{"browser read-only action"},
			}
		case "screenshot":
			// A picture is a new file in the workspace, but nothing on the page
			// or the host changes state the user cares about.
			return Verdict{
				Level: Allow, Source: LocalSource, Confidence: 1, Risk: "low",
				Reasons: []string{"browser screenshot writes one file in the workspace"},
			}
		case "open", "navigate", "click", "dblclick", "hover", "focus", "check", "uncheck",
			"fill", "type", "select", "press", "insert", "scroll", "scroll_into_view",
			"tab_new", "tab_close", "tab_select", "dialog_accept", "dialog_dismiss":
			// open starts a browser and Chromium; the rest type into a real page,
			// which can submit a form, delete an account or post a comment. The
			// session reviewer sees the arguments and the conversation; a fixed
			// table cannot.
			return f.next(ctx, tool, args)
		case "eval", "upload":
			// Neither is in the tool schema. A call that carries one is either a
			// mistake or an injection attempt, and neither is worth reviewing.
			return Verdict{Level: Deny, Source: LocalSource, Confidence: 1, Risk: "high",
				Reasons: []string{"browser eval/upload is not an available action"}}
		}
		return f.failClosed("unknown browser action")
	case "visual":
		var a struct {
			Action string `json:"action"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return f.failClosed("visual has no action")
		}
		switch a.Action {
		case "chart":
			// A chart is computed here and written, at most, inside the
			// workspace. Nothing external runs.
			return Verdict{
				Level: Allow, Source: LocalSource, Confidence: 1, Risk: "low",
				Reasons: []string{"visual chart renders local data only"},
			}
		case "diagram":
			// It runs the installed tuios binary on our source, which is the
			// renderer, not user code. The output stays in the workspace.
			return Verdict{
				Level: Allow, Source: LocalSource, Confidence: 1, Risk: "low",
				Reasons: []string{"visual diagram renders Mermaid through tuios"},
			}
		case "dashboard_open", "dashboard_patch", "dashboard_close":
			// A TML surface can carry buttons and actions a person presses.
			// The reviewer sees the document.
			return f.next(ctx, tool, args)
		}
		return f.failClosed("unknown visual action")
	case "workspace":
		var a struct {
			Action string `json:"action"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return f.failClosed("workspace has no action")
		}
		switch a.Action {
		case "list", "snapshot":
			return Verdict{
				Level: Allow, Source: LocalSource, Confidence: 1, Risk: "low",
				Reasons: []string{"workspace reads the arranged panes"},
			}
		case "create", "focus", "close":
			// Starting commands, stealing focus and closing panes all change
			// what the person is looking at. The reviewer decides.
			return f.next(ctx, tool, args)
		}
		return f.failClosed("unknown workspace action")
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
