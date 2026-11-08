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
	// The floor applies even when this classifier is used without Chain.
	if v, bad := staticDeny(tool, args); bad {
		return v
	}
	switch tool {
	case "terminal":
		var a struct {
			Action  string `json:"action"`
			Text    string `json:"text"`
			Command string `json:"command"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return f.failClosed("terminal has no action")
		}
		switch a.Action {
		case "list", "capture", "wait":
			return Verdict{Level: Allow, Source: LocalSource, Confidence: 1, Risk: "low", Reasons: []string{"GUI terminal read, subject to pane access"}}
		case "send":
			if a.Text == "" {
				return f.failClosed("terminal send has no text")
			}
			return f.next(ctx, tool, args)
		case "run":
			if strings.TrimSpace(a.Command) == "" {
				return f.failClosed("terminal run has no command")
			}
			return f.next(ctx, tool, args)
		case "new", "split", "press", "focus", "resize", "rename", "close":
			return f.next(ctx, tool, args)
		}
		return f.failClosed("unknown terminal action")
	case "tuios":
		var a struct {
			Action      string   `json:"action"`
			Command     string   `json:"command"`
			Script      string   `json:"script"`
			Text        string   `json:"text"`
			Submit      bool     `json:"submit"`
			Argv        []string `json:"argv"`
			HostControl bool     `json:"host_control"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return f.failClosed("tuios has no action")
		}
		switch a.Action {
		case "list", "capture", "wait":
			if a.HostControl {
				return f.next(ctx, tool, args)
			}
			return Verdict{
				Level: Allow, Source: LocalSource, Confidence: 1, Risk: "low",
				Reasons: []string{"tuios read-only action"},
			}
		case "run":
			hasCommand := strings.TrimSpace(a.Command) != ""
			hasScript := strings.TrimSpace(a.Script) != ""
			switch {
			case !hasCommand && !hasScript:
				return f.failClosed("tuios run has no command or script")
			case hasCommand && hasScript:
				return f.failClosed("tuios run cannot use command and script together")
			}
			// Keep the real tool, target, cwd and capabilities visible to review.
			return f.next(ctx, tool, args)
		case "send":
			if strings.TrimSpace(a.Text) == "" && !a.Submit {
				return f.failClosed("tuios send has no text")
			}
			return f.next(ctx, tool, args)
		case "press":
			// Keys go into a pane that may be running anything; the reviewer
			// sees which key and which pane.
			return f.next(ctx, tool, args)
		case "screenshot":
			// out can overwrite a project file; this is a write, not a pane read.
			return f.next(ctx, tool, args)
		case "new":
			// Even an empty shell establishes a lasting cwd/network capability
			// ceiling. It is not a read-only action or a local unconditional allow.
			return f.next(ctx, tool, args)
		case "focus":
			if a.HostControl {
				return f.next(ctx, tool, args)
			}
			return Verdict{Level: Confirm, Source: LocalSource, Confidence: 1, Risk: "low",
				Reasons: []string{"focusing an existing pane"}}
		case "move", "resize", "rename", "minimize":
			if a.HostControl {
				return f.next(ctx, tool, args)
			}
			return Verdict{Level: Confirm, Source: LocalSource, Confidence: 1, Risk: "low",
				Reasons: []string{"changing an existing pane"}}
		case "claim":
			return f.next(ctx, tool, args)
		case "revoke":
			return Verdict{Level: Allow, Source: LocalSource, Confidence: 1, Risk: "low",
				Reasons: []string{"revoking this agent session's pane control lease"}}
		case "close":
			// Closing a pane can kill a running task; leave that to the
			// contextual reviewer rather than a fixed local allow.
			return f.next(ctx, tool, args)
		}
		return f.failClosed("unknown tuios action")
	case "browser":
		var a struct {
			Action      string `json:"action"`
			HostControl bool   `json:"host_control"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return f.failClosed("browser has no action")
		}
		switch a.Action {
		case "list", "snapshot", "find", "get", "is", "wait", "dialog",
			"console", "errors", "network", "status", "pause":
			if a.HostControl {
				return f.next(ctx, tool, args)
			}
			// Observing a page changes nothing on the host or on the page.
			return Verdict{
				Level: Allow, Source: LocalSource, Confidence: 1, Risk: "low",
				Reasons: []string{"browser read-only action"},
			}
		case "screenshot":
			return f.next(ctx, tool, args)
		case "open", "attach", "navigate", "click", "dblclick", "hover", "focus", "check", "uncheck",
			"fill", "type", "select", "press", "insert", "scroll", "scroll_into_view",
			"tab_new", "tab_close", "tab_select", "dialog_accept", "dialog_dismiss", "resume", "quit":
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
	case "computer_request":
		var a struct {
			Application string `json:"application"`
		}
		if json.Unmarshal(args, &a) != nil || strings.TrimSpace(a.Application) == "" {
			return f.failClosed("computer request has no application")
		}
		// The frontend resolves the name against local applications and asks
		// the person if it cannot uniquely determine the app or window.
		return Verdict{Level: Confirm, Source: LocalSource, Confidence: 1, Risk: "medium",
			Reasons: []string{"resolve and possibly launch the requested desktop application"}}
	case "computer":
		var a struct {
			Action string `json:"action"`
		}
		if json.Unmarshal(args, &a) != nil {
			return f.failClosed("computer has no action")
		}
		switch a.Action {
		case "status", "snapshot", "screenshot", "pause", "stop":
			return Verdict{Level: Allow, Source: LocalSource, Confidence: 1, Risk: "low",
				Reasons: []string{"observe or stop the current target-bound desktop session"}}
		case "click", "click_at", "fill", "press", "scroll":
			// A desktop action may have external or destructive effects. The
			// contextual reviewer still evaluates the requested operation.
			return f.next(ctx, tool, args)
		}
		return f.failClosed("unknown computer action")
	case "workspace":
		var a struct {
			Action      string `json:"action"`
			HostControl bool   `json:"host_control"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return f.failClosed("workspace has no action")
		}
		switch a.Action {
		case "list", "snapshot":
			if a.HostControl {
				return f.next(ctx, tool, args)
			}
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

func (f FirstParty) next(ctx context.Context, tool string, args json.RawMessage) Verdict {
	if f.Next == nil {
		return f.failClosed("no reviewer behind the first-party classifier")
	}
	return f.Next.Classify(ctx, tool, args)
}

func (f FirstParty) failClosed(reason string) Verdict {
	return Verdict{Level: Confirm, Failed: true, Source: "static", Reasons: []string{reason}}
}
