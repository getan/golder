package mygoui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/judge"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/seatbelt"
)

type terminalToolArgs struct {
	Action        string   `json:"action"`
	Pane          string   `json:"pane,omitempty"`
	Name          string   `json:"name,omitempty"`
	Direction     string   `json:"direction,omitempty"`
	Argv          []string `json:"argv,omitempty"`
	Network       string   `json:"network,omitempty"`
	Text          string   `json:"text,omitempty"`
	Command       string   `json:"command,omitempty"`
	Keys          string   `json:"keys,omitempty"`
	Lines         int      `json:"lines,omitempty"`
	Revision      uint64   `json:"revision,omitempty"`
	TimeoutMS     int      `json:"timeout_ms,omitempty"`
	Percent       float32  `json:"percent,omitempty"`
	Cwd           string   `json:"cwd,omitempty"`
	HostControl   bool     `json:"host_control,omitempty"`
	ControlOrigin string   `json:"control_origin,omitempty"`
	ControlScope  string   `json:"control_scope,omitempty"`
	Isolated      bool     `json:"isolated,omitempty"`
	Remote        bool     `json:"remote,omitempty"`
}

// terminalTool is bound to one controller incarnation and one project. It
// never resolves a target from focus, a title, or model-supplied identity.
type terminalTool struct {
	app      *App
	agent    *terminalAgent
	guard    *agenttool.ControlGuard
	dispatch func(context.Context, func() (any, error)) (any, error)
}

func (t *terminalTool) Name() string { return "terminal" }
func (t *terminalTool) Description() string {
	return "Open and manage Golder GUI's built-in terminal panel. For 'open a terminal' or '打开终端', use action=new here by default, " +
		"not computer_request or a desktop terminal app, unless the user explicitly requests a system terminal. " +
		"Use list/focus for an existing authorized pane, split for another pane; ordinary noninteractive commands still use bash. " +
		"list returns exact pane IDs and your access; capture reads the last lines; " +
		"wait observes output revisions without polling. new and split create Agent-owned panes with a fixed sandbox/network ceiling (full-access may be unconfined); " +
		"send, press and run write only to a pane currently assigned to this Agent. " +
		"A person's pane is outside the Agent sandbox and needs explicit host/network review; read and input grants are separate. " +
		"Network-enabled interactive input may reach a remote host; local sandboxing cannot contain remote effects. " +
		"run requires a known local shell prompt, sends one line and does not prove completion. Never retry automatically when execution state is unknown; " +
		"if PTY allocation is denied, use a noninteractive alternative rather than escalating or retrying it."
}
func (t *terminalTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"required":["action"],"properties":{
		"action":{"type":"string","enum":["list","capture","wait","new","split","send","press","run","focus","resize","rename","close"]},
		"pane":{"type":"string","maxLength":128,"description":"Exact pane ID from list; required except for list and new."},
		"name":{"type":"string","maxLength":100},
		"direction":{"type":"string","enum":["right","down"]},
		"argv":{"type":"array","maxItems":32,"items":{"type":"string","maxLength":4096}},
		"network":{"type":"string","enum":["off","on"]},
		"text":{"type":"string","maxLength":8192},
		"command":{"type":"string","maxLength":2048},
		"keys":{"type":"string","maxLength":512},
		"lines":{"type":"integer","minimum":1,"maximum":1000},
		"revision":{"type":"integer","minimum":0},
		"timeout_ms":{"type":"integer","minimum":100,"maximum":120000},
		"percent":{"type":"number","minimum":10,"maximum":90},
		"cwd":{"type":"string","description":"Prepared by the tool; not a model-supplied capability."},
		"host_control":{"type":"boolean","description":"Prepared target fact, not a model-supplied capability."},
		"control_origin":{"type":"string","enum":["agent-created","operator-claimed"]},
		"control_scope":{"type":"string","enum":["session"]},
		"isolated":{"type":"boolean","description":"Prepared target fact, not a model-supplied capability."},
		"remote":{"type":"boolean","description":"Prepared fact: the operation may reach a remote host."}
	}}`)
}
func (t *terminalTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionParallel
}

func (t *terminalTool) ui(ctx context.Context, fn func() (any, error)) (any, error) {
	if t.dispatch != nil {
		return t.dispatch(ctx, fn)
	}
	return t.app.terminalOnUI(ctx, fn)
}

func (t *terminalTool) checkSession() (*permissions.State, error) {
	return t.agent.controller.CheckGUITerminalSession(t.agent.session, t.agent.project)
}

func terminalReadAction(action string) bool {
	return action == "list" || action == "capture" || action == "wait"
}

func (t *terminalTool) PrepareCall(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var req terminalToolArgs
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if _, err := t.checkSession(); err != nil {
		return nil, err
	}
	// These facts come from the selected pane, never from model arguments.
	req.Cwd, req.ControlOrigin, req.ControlScope = "", "", ""
	req.HostControl, req.Isolated, req.Remote = false, false, false
	if len(req.Pane) > 128 || len(req.Name) > 100 || len(req.Text) > 8192 || len(req.Command) > 2048 || len(req.Keys) > 512 || len(req.Argv) > 32 {
		return nil, errors.New("terminal: argument exceeds the reviewable size limit")
	}
	if req.Network != "" && req.Network != "off" && req.Network != "on" {
		return nil, errors.New("terminal: network must be off or on")
	}
	argvBytes := 0
	for _, arg := range req.Argv {
		argvBytes += len(arg)
		if len(arg) > 4096 {
			return nil, errors.New("terminal: argv element exceeds the reviewable size limit")
		}
		if strings.ContainsRune(arg, '\x00') {
			return nil, errors.New("terminal: argv contains NUL")
		}
	}
	if argvBytes > 8192 {
		return nil, errors.New("terminal: argv exceeds the reviewable size limit")
	}
	if terminalReadAction(req.Action) {
		req.Network = ""
	}
	if req.Name != "" {
		for _, r := range req.Name {
			if unicode.IsControl(r) {
				return nil, errors.New("terminal: name contains control characters")
			}
		}
	}
	switch req.Action {
	case "list":
	case "new":
		req.Cwd = t.agent.project
	case "split", "capture", "wait", "send", "press", "run", "focus", "resize", "rename", "close":
		if req.Pane == "" {
			return nil, errors.New("terminal: an exact pane ID from list is required")
		}
		value, err := t.ui(ctx, func() (any, error) {
			p := t.app.findTerminalPane(t.agent.scope(), req.Pane)
			if p == nil || p.attached || p.conn == nil || p.term == nil {
				return nil, errors.New("terminal: pane is unavailable or is an attached bash TTY")
			}
			read, write, _ := t.app.terminalRegistry().Access(p.id, t.agent.id)
			canClose := req.Action == "close" && t.app.terminalRegistry().CanClose(p.id, t.agent.id)
			if terminalReadAction(req.Action) && !read || !terminalReadAction(req.Action) && !write && !canClose {
				return nil, errors.New("terminal: this Agent has no permission for the requested pane action")
			}
			return p, nil
		})
		if err != nil {
			return nil, err
		}
		p := value.(*terminalPane)
		req.ControlScope = "session"
		if p.creator == t.agent.id {
			req.ControlOrigin = "agent-created"
		} else {
			req.ControlOrigin = "operator-claimed"
		}
		if p.envelope != nil {
			req.Isolated = p.envelope.Isolated
			req.Cwd = t.agent.project
			if req.Action == "run" && !p.envelope.Shell {
				return nil, errors.New("terminal: run requires an Agent shell; use send for an interactive program")
			}
			if req.Action == "run" && p.envelope.Network {
				return nil, errors.New("terminal: cannot pin the working directory in a network-enabled interactive pane; use reviewed send/press")
			}
			if req.Action == "send" || req.Action == "press" || req.Action == "run" {
				if p.envelope.Network {
					req.Network = "on"
					req.Remote = true
				} else if req.Network == "on" {
					return nil, errors.New("terminal: network cannot be added to an existing offline pane")
				}
			}
		} else if !terminalReadAction(req.Action) {
			req.HostControl = true
			if req.Action == "split" {
				req.Cwd = t.agent.project // the new child, not the user's shell
			}
			if req.Action == "send" || req.Action == "press" || req.Action == "run" {
				req.Network = "on" // a user shell can be remote or network-enabled
				req.Remote = true
			}
		}
	default:
		return nil, errors.New("terminal: unknown action")
	}
	if (req.Action == "new" || req.Action == "split") && req.Network == "on" && len(req.Argv) > 0 {
		req.Remote = true // an initial networked program may act on a remote host
	}
	if req.Action == "split" && req.Direction != "right" && req.Direction != "down" {
		return nil, errors.New("terminal: split requires direction right or down")
	}
	if req.Action == "send" && req.Text == "" {
		return nil, errors.New("terminal: send requires input")
	}
	if req.Action == "run" && (strings.TrimSpace(req.Command) == "" || strings.ContainsFunc(req.Command, unicode.IsControl)) {
		return nil, errors.New("terminal: run requires one line without control characters")
	}
	if req.Action == "press" {
		if _, err := terminalKeys(req.Keys); err != nil {
			return nil, err
		}
	}
	if req.Action == "rename" && strings.TrimSpace(req.Name) == "" {
		return nil, errors.New("terminal: rename requires a name")
	}
	if req.Action == "resize" && (req.Percent < 10 || req.Percent > 90) {
		return nil, errors.New("terminal: resize requires percent between 10 and 90")
	}
	return json.Marshal(req)
}

// User-owned terminals can be displaying arbitrary prompts or remote hosts.
// A standing GUI lease is not permission to answer such prompts invisibly.
func (t *terminalTool) RequiresOperatorConfirmationFor(raw json.RawMessage) bool {
	var req terminalToolArgs
	_ = json.Unmarshal(raw, &req)
	return req.HostControl && (req.Action == "send" || req.Action == "run" || req.Action == "press" || req.Action == "close")
}

func terminalError(err error) (agentcore.AgentToolResult, error) {
	return agentcore.AgentToolResult{IsError: true, Content: agentcore.ContentList{agentcore.NewTextContent(err.Error())}}, nil
}

func terminalResult(value any) (agentcore.AgentToolResult, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return terminalError(err)
	}
	return agentcore.AgentToolResult{Content: agentcore.ContentList{agentcore.NewTextContent(string(b))}}, nil
}

func (t *terminalTool) Execute(ctx context.Context, callID string, raw json.RawMessage, _ agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	var req terminalToolArgs
	if err := json.Unmarshal(raw, &req); err != nil {
		return terminalError(err)
	}
	if !agentcore.ExternalToolAuthorized(ctx) {
		return terminalError(errors.New("terminal: the host authorization gate was not applied"))
	}
	if floor := (judge.Chain{}).Classify(ctx, "terminal", raw); floor.Level == judge.Deny {
		return terminalError(fmt.Errorf("terminal: blocked by static policy: %s", strings.Join(floor.Reasons, "; ")))
	}
	mode, err := t.checkSession()
	if err != nil {
		return terminalError(err)
	}
	if mode != nil && mode.Mode() == permissions.ReadOnly && !terminalReadAction(req.Action) {
		return terminalError(errors.New("terminal: read-only mode does not permit control"))
	}
	if t.RequiresOperatorConfirmationFor(raw) && !agentcore.OperatorConfirmedFor(ctx, callID, "terminal", raw) {
		return terminalError(errors.New("terminal: exact operator confirmation is required"))
	}
	switch req.Action {
	case "list":
		value, err := t.ui(ctx, func() (any, error) { return t.app.terminalStatuses(t.agent), nil })
		if err != nil {
			return terminalError(err)
		}
		return terminalResult(map[string]any{"panes": value})
	case "new", "split":
		return t.create(ctx, req)
	case "capture", "wait", "send", "press", "run", "focus", "resize", "rename", "close":
		value, err := t.ui(ctx, func() (any, error) {
			p := t.app.findTerminalPane(t.agent.scope(), req.Pane)
			if p == nil || p.attached || p.conn == nil || p.term == nil {
				return nil, errors.New("terminal: pane is no longer available")
			}
			return p, nil
		})
		if err != nil {
			return terminalError(err)
		}
		p := value.(*terminalPane)
		if err := t.checkTarget(ctx, p, req); err != nil {
			return terminalError(err)
		}
		return t.executePane(ctx, p, req)
	default:
		return terminalError(errors.New("terminal: unknown action"))
	}
}

func (t *terminalTool) checkTarget(ctx context.Context, p *terminalPane, req terminalToolArgs) error {
	if p.id != req.Pane || p.scope() != t.agent.scope() {
		return errors.New("terminal: reviewed pane identity changed")
	}
	if req.Remote && (agentcore.SandboxRequestedFromContext(ctx) || seatbelt.ModeFromEnv() == seatbelt.ModeEnforce) {
		return errors.New("terminal: local sandbox containment cannot guarantee remote effects")
	}
	if p.envelope == nil {
		if req.ControlOrigin != "operator-claimed" || req.Isolated {
			return errors.New("terminal: reviewed pane origin changed")
		}
		if agentcore.SandboxRequestedFromContext(ctx) || seatbelt.ModeFromEnv() == seatbelt.ModeEnforce {
			return errors.New("terminal: a user terminal cannot satisfy required OS containment")
		}
		if !terminalReadAction(req.Action) {
			mode, err := t.checkSession()
			if err != nil {
				return err
			}
			if !req.HostControl || !agentcore.HostControlGrantedFromContext(ctx) && (mode == nil || mode.Mode() != permissions.FullAccess) {
				return errors.New("terminal: reviewed host control is required")
			}
			if (req.Action == "send" || req.Action == "press" || req.Action == "run") && !agentcore.NetworkGrantedFromContext(ctx) && (mode == nil || mode.Mode() != permissions.FullAccess) {
				return errors.New("terminal: user terminal input requires reviewed network authority")
			}
		}
		return nil
	}
	if req.Isolated != p.envelope.Isolated || req.ControlOrigin == "agent-created" && p.creator != t.agent.id {
		return errors.New("terminal: reviewed pane origin changed")
	}
	if !terminalReadAction(req.Action) {
		if req.Action == "send" || req.Action == "press" || req.Action == "run" {
			return t.guard.CheckGUITerminalInput(ctx, *p.envelope)
		}
		return t.guard.CheckGUITerminalControl(ctx, *p.envelope)
	}
	if !p.envelope.Isolated && agentcore.SandboxRequestedFromContext(ctx) {
		return errors.New("terminal: pane cannot satisfy required OS containment")
	}
	return nil
}

func terminalKeys(keys string) ([]byte, error) {
	var out []byte
	for _, key := range strings.Fields(strings.ReplaceAll(keys, ",", " ")) {
		switch strings.ToLower(key) {
		case "enter", "return":
			out = append(out, '\r')
		case "tab":
			out = append(out, '\t')
		case "esc", "escape":
			out = append(out, 0x1b)
		case "backspace":
			out = append(out, 0x7f)
		case "up":
			out = append(out, "\x1b[A"...)
		case "down":
			out = append(out, "\x1b[B"...)
		case "right":
			out = append(out, "\x1b[C"...)
		case "left":
			out = append(out, "\x1b[D"...)
		default:
			lower := strings.ToLower(key)
			if len(lower) == 6 && strings.HasPrefix(lower, "ctrl+") && lower[5] >= 'a' && lower[5] <= 'z' {
				out = append(out, lower[5]-'a'+1)
			} else {
				return nil, fmt.Errorf("terminal: unsupported key %q", key)
			}
		}
	}
	if len(out) == 0 || len(out) > 2048 {
		return nil, errors.New("terminal: keys must contain a bounded supported sequence")
	}
	return out, nil
}

func terminalTail(text string, lines int) string {
	if lines <= 0 {
		lines = 80
	}
	parts := strings.Split(text, "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	text = strings.Join(parts, "\n")
	if len(text) > 50000 {
		text = text[len(text)-50000:]
		for len(text) > 0 && !utf8.RuneStart(text[0]) {
			text = text[1:]
		}
		text = "[earlier output omitted]\n" + text
	}
	return text
}

func terminalTimeout(ms int) time.Duration {
	if ms <= 0 {
		return 30 * time.Second
	}
	return time.Duration(min(ms, 120000)) * time.Millisecond
}
