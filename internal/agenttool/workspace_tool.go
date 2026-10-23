package agenttool

// The workspace tool arranges the panes a full-stack or data session needs and
// then reads them back as one context snapshot. It is the only tool that knows
// about several panes at once: the terminal and browser tools each act on one.
//
// A preset is a small plan (name, kind, argv), and create starts each pane
// through the daemon. The browser pane carries its own control socket under
// .golder/browser, so the browser tool and the snapshot both find it without a
// second configuration.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/browserctl"
	"github.com/getan/golder/internal/tuiosctl"
)

// Workspace presets. They are names rather than free-form layouts so a typo is
// a clear error, and each one has a concrete debugging use.
const (
	PresetFullStack = "debug-fullstack"
	PresetData      = "data-analysis"
	PresetDiagram   = "diagram"
	PresetReview    = "review"
)

// workspacePane is one pane the tool started.
type workspacePane struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"` // terminal or browser
	WindowID string `json:"window_id"`
	Socket   string `json:"socket,omitempty"`
	URL      string `json:"url,omitempty"`
}

// paneSpec is one line of a preset plan.
type paneSpec struct {
	Name   string
	Kind   string
	Argv   []string
	Socket string
	URL    string
}

// NewWorkspaceTool builds the facade. allowControl=false keeps list and
// snapshot: a sub-agent can read the session's panes but cannot create,
// focus or close them.
func NewWorkspaceTool(cwd string, allowControl bool) *WorkspaceTool {
	return &WorkspaceTool{
		cwd: cwd, allowControl: allowControl,
		panes: map[string]workspacePane{},
		newClient: func(ctx context.Context) (*tuiosctl.Client, error) {
			return tuiosctl.Dial(ctx, tuiosctl.FromEnv())
		},
		newBrowser: func(socket string) (*browserctl.Client, error) {
			return browserctl.Dial(socket)
		},
	}
}

// WorkspaceTool arranges and reads several panes at once.
type WorkspaceTool struct {
	cwd          string
	allowControl bool

	mu         sync.Mutex
	panes      map[string]workspacePane
	newClient  func(context.Context) (*tuiosctl.Client, error)
	newBrowser func(string) (*browserctl.Client, error)

	// capture and browserSnapshot are seams for tests; nil uses the daemon and
	// the browser socket.
	capture         func(ctx context.Context, window string, lines int) (string, error)
	browserSnapshot func(ctx context.Context, socket string) (map[string]any, error)
}

func (t *WorkspaceTool) Name() string { return "workspace" }

func (t *WorkspaceTool) Description() string {
	if t.allowControl {
		return "Arrange the panes a session needs (debug-fullstack, data-analysis, diagram, review) " +
			"and read them back as one snapshot: terminal tails plus the browser's page, console and errors. " +
			"create/list/snapshot/focus/close."
	}
	return "Read the arranged panes as one snapshot: terminal tails plus the browser's page, console and errors. list/snapshot only."
}

func (t *WorkspaceTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionParallel
}

const workspaceReadSchema = `{
  "type":"object","additionalProperties":false,"required":["action"],
  "properties":{
    "action":{"type":"string","enum":["list","snapshot"]},
    "lines":{"type":"integer","minimum":1,"maximum":500,"description":"For snapshot: terminal lines per pane; default 60."}
  }
}`

const workspaceControlSchema = `{
  "type":"object","additionalProperties":false,"required":["action"],
  "properties":{
    "action":{"type":"string","enum":["create","list","snapshot","focus","close"]},
    "preset":{"type":"string","enum":["debug-fullstack","data-analysis","diagram","review"],"description":"For create."},
    "name":{"type":"string","description":"For focus/close: the pane name from list."},
    "url":{"type":"string","description":"For create: the page the browser pane opens; default http://localhost:3000."},
    "backend":{"type":"string","description":"For create with debug-fullstack: the backend command."},
    "frontend":{"type":"string","description":"For create with debug-fullstack: the frontend command."},
    "lines":{"type":"integer","minimum":1,"maximum":500,"description":"For snapshot: terminal lines per pane; default 60."}
  }
}`

type workspaceArgs struct {
	Action   string `json:"action"`
	Preset   string `json:"preset,omitempty"`
	Name     string `json:"name,omitempty"`
	URL      string `json:"url,omitempty"`
	Backend  string `json:"backend,omitempty"`
	Frontend string `json:"frontend,omitempty"`
	Lines    int    `json:"lines,omitempty"`
}

func (t *WorkspaceTool) Schema() json.RawMessage {
	if !t.allowControl {
		return json.RawMessage(workspaceReadSchema)
	}
	return json.RawMessage(workspaceControlSchema)
}

func (t *WorkspaceTool) Execute(ctx context.Context, _ string, raw json.RawMessage, _ agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	var a workspaceArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return workspaceError(err), nil
		}
	}
	switch a.Action {
	case "create":
		if !t.allowControl {
			return workspaceError(errors.New("creating panes is not available to sub-agents")), nil
		}
		return t.create(ctx, a)
	case "list":
		return t.list(), nil
	case "snapshot":
		return t.snapshot(ctx, a)
	case "focus", "close":
		if !t.allowControl {
			return workspaceError(errors.New("pane control is not available to sub-agents")), nil
		}
		return t.control(ctx, a)
	case "":
		return workspaceError(errors.New("action is required")), nil
	default:
		return workspaceError(fmt.Errorf("unknown action %q", a.Action)), nil
	}
}

func (t *WorkspaceTool) create(ctx context.Context, a workspaceArgs) (agentcore.AgentToolResult, error) {
	specs, err := t.presetPlan(a)
	if err != nil {
		return workspaceError(err), nil
	}
	c, err := t.newClient(ctx)
	if err != nil {
		return workspaceError(err), nil
	}
	defer c.Close()
	started := make([]workspacePane, 0, len(specs))
	for _, spec := range specs {
		res, err := c.NewWindow(ctx, tuiosctl.NewWindowParams{
			Name: spec.Name, Cwd: t.cwd, Command: spec.Argv, Focus: false, CloseOnExit: true,
		})
		if err != nil {
			return workspaceError(fmt.Errorf("start pane %q: %w", spec.Name, err)), nil
		}
		pane := workspacePane{Name: spec.Name, Kind: spec.Kind, WindowID: res.WindowID, Socket: spec.Socket, URL: spec.URL}
		if spec.Socket != "" {
			// The browser creates the socket at startup; a snapshot that races
			// it would report "not running" for a pane that is about to work.
			waitForSocket(spec.Socket, 8*time.Second)
			// Now that a browser exists, the browser tool can use it without the
			// person exporting KITTY_BROWSE_SOCKET and restarting.
			setBrowserSocket(spec.Socket)
		}
		t.mu.Lock()
		t.panes[spec.Name] = pane
		t.mu.Unlock()
		started = append(started, pane)
	}
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(tuiosJSON(map[string]any{
			"preset": a.Preset, "panes": started,
		}))},
		Details: map[string]any{"preset": a.Preset, "panes": len(started)},
	}, nil
}

// presetPlan turns a preset name into the panes to start. It is pure, so the
// layout is testable without a daemon.
func (t *WorkspaceTool) presetPlan(a workspaceArgs) ([]paneSpec, error) {
	url := strings.TrimSpace(a.URL)
	if url == "" {
		url = "http://localhost:3000"
	}
	browser := func(name string) (paneSpec, error) {
		return browserPaneSpec(t.cwd, name, url)
	}
	shell := func(name, command string) paneSpec {
		spec := paneSpec{Name: name, Kind: "terminal"}
		if strings.TrimSpace(command) != "" {
			spec.Argv = []string{"sh", "-lc", command}
		}
		return spec
	}
	switch a.Preset {
	case PresetFullStack:
		b, err := browser("browser")
		if err != nil {
			return nil, err
		}
		return []paneSpec{
			shell("backend", a.Backend),
			shell("frontend", a.Frontend),
			b,
		}, nil
	case PresetData:
		return []paneSpec{shell("analysis", "")}, nil
	case PresetDiagram:
		return []paneSpec{shell("diagram", "")}, nil
	case PresetReview:
		b, err := browser("browser")
		if err != nil {
			return nil, err
		}
		return []paneSpec{shell("tests", ""), b}, nil
	case "":
		return nil, errors.New("create needs a preset: debug-fullstack, data-analysis, diagram or review")
	}
	return nil, fmt.Errorf("unknown preset %q", a.Preset)
}

func (t *WorkspaceTool) list() agentcore.AgentToolResult {
	t.mu.Lock()
	panes := make([]workspacePane, 0, len(t.panes))
	for _, p := range t.panes {
		panes = append(panes, p)
	}
	t.mu.Unlock()
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(tuiosJSON(map[string]any{"panes": panes}))},
		Details: map[string]any{"panes": len(panes)},
	}
}

// snapshot reads every arranged pane as one context block. A terminal pane
// contributes its tail; a browser pane contributes the page identity, an
// interactive snapshot and the error buffers. Output is bounded per pane so one
// chatty command cannot crowd out the rest.
func (t *WorkspaceTool) snapshot(ctx context.Context, a workspaceArgs) (agentcore.AgentToolResult, error) {
	lines := a.Lines
	if lines <= 0 {
		lines = 60
	}
	if lines > 500 {
		lines = 500
	}
	t.mu.Lock()
	panes := make([]workspacePane, 0, len(t.panes))
	for _, p := range t.panes {
		panes = append(panes, p)
	}
	t.mu.Unlock()
	if len(panes) == 0 {
		return workspaceError(errors.New("no panes have been arranged; call create first")), nil
	}
	out := make([]map[string]any, 0, len(panes))
	for _, p := range panes {
		entry := map[string]any{"name": p.Name, "kind": p.Kind, "window_id": p.WindowID}
		switch p.Kind {
		case "browser":
			snap, err := t.browserState(ctx, p.Socket)
			if err != nil {
				entry["error"] = err.Error()
			} else {
				entry["browser"] = snap
			}
		default:
			text, err := t.terminalTail(ctx, p.WindowID, lines)
			if err != nil {
				entry["error"] = err.Error()
			} else {
				entry["tail"] = tuiosUntrusted(text)
			}
		}
		out = append(out, entry)
	}
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(tuiosTruncate(tuiosJSON(map[string]any{"panes": out})))},
		Details: map[string]any{"panes": len(out)},
	}, nil
}

func (t *WorkspaceTool) terminalTail(ctx context.Context, window string, lines int) (string, error) {
	if t.capture != nil {
		return t.capture(ctx, window, lines)
	}
	c, err := t.newClient(ctx)
	if err != nil {
		return "", err
	}
	defer c.Close()
	cap, err := c.CapturePane(ctx, window, "recent", lines, false)
	if err != nil {
		return "", err
	}
	return cap.Content, nil
}

// browserState reads the page and the diagnostics a debugger needs: what is
// open, the actionable tree, and the console/network errors.
func (t *WorkspaceTool) browserState(ctx context.Context, socket string) (map[string]any, error) {
	if t.browserSnapshot != nil {
		return t.browserSnapshot(ctx, socket)
	}
	c, err := t.newBrowser(socket)
	if err != nil {
		return nil, err
	}
	info, err := c.Info(ctx)
	if err != nil {
		return nil, err
	}
	target := browserctl.Target{BrowserID: info.BrowserID}
	for _, tab := range info.Tabs {
		if tab.Active || tab.ID == info.ActiveTabID {
			target.TabID, target.TargetID = tab.ID, tab.TargetID
		}
	}
	snap, meta, err := c.Action(ctx, "snapshot", map[string]any{"interactive": true, "compact": true}, target, 30*time.Second)
	if err != nil {
		return nil, err
	}
	state := map[string]any{"url": meta.URL, "title": meta.Title, "snapshot": json.RawMessage(snap)}
	if errs, _, err := c.Action(ctx, "errors", map[string]any{"limit": 20}, target, 30*time.Second); err == nil {
		state["errors"] = json.RawMessage(errs)
	}
	if net, _, err := c.Action(ctx, "network.requests", map[string]any{"limit": 20}, target, 30*time.Second); err == nil {
		state["network"] = json.RawMessage(net)
	}
	return state, nil
}

func (t *WorkspaceTool) control(ctx context.Context, a workspaceArgs) (agentcore.AgentToolResult, error) {
	name := strings.TrimSpace(a.Name)
	if name == "" {
		return workspaceError(errors.New("focus/close needs name")), nil
	}
	t.mu.Lock()
	pane, ok := t.panes[name]
	t.mu.Unlock()
	if !ok {
		return workspaceError(fmt.Errorf("no pane named %q; call list", name)), nil
	}
	c, err := t.newClient(ctx)
	if err != nil {
		return workspaceError(err), nil
	}
	defer c.Close()
	if a.Action == "focus" {
		if _, err := c.FocusWindow(ctx, pane.WindowID); err != nil {
			return workspaceError(err), nil
		}
		return agentcore.AgentToolResult{
			Content: agentcore.ContentList{agentcore.NewTextContent(tuiosJSON(map[string]any{"focused": name, "window_id": pane.WindowID}))},
			Details: map[string]any{"window_id": pane.WindowID},
		}, nil
	}
	if own := c.PaneID(); own != "" && own == pane.WindowID {
		return workspaceError(errors.New("refusing to close the pane this agent runs in")), nil
	}
	if err := c.CloseWindow(ctx, pane.WindowID); err != nil {
		return workspaceError(err), nil
	}
	t.mu.Lock()
	delete(t.panes, name)
	t.mu.Unlock()
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(tuiosJSON(map[string]any{"closed": name}))},
		Details: map[string]any{"window_id": pane.WindowID},
	}, nil
}

func workspaceError(err error) agentcore.AgentToolResult {
	return errorResult("workspace: " + err.Error())
}
