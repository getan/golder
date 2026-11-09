package agenttool

// The browser tool drives one running browser through its typed control socket
// — golder's own `browse` pane or an external kitty-browse. It is not a second
// browser: nothing here launches Chromium or speaks CDP. The browser owns the
// page; this only asks for one action at a time, with the tab pinned to the ids
// /v1/info reported.
//
// Read-only actions are safe to run alone: snapshot, find, get, is, wait,
// dialog.status, console, errors, network, screenshot. Control actions put
// input into the page and are left to the reviewer. eval and upload are not
// part of the schema at all.
//
// open is the setup step: it starts a browser with the profile and socket it
// needs, so nobody has to remember the three-command invocation.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "image/png"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/browserctl"
	"github.com/getan/golder/internal/controlcap"
	"github.com/getan/golder/internal/permissions"
	"github.com/getan/golder/internal/tuiosctl"
)

const browserMaxTimeout = 120 * time.Second

// A full-page PNG can be tiny on disk and enormous once decoded. The browser
// client already caps transport bytes; this cap covers the display-side decode
// and the artifact's downstream view_image use. A screenshot that fails to
// decode is left to the browser's own contract (the client only returns 2xx).
const browserScreenshotMaxPixels = 16_000_000

// BrowserAvailable reports whether this process should advertise the tool.
func BrowserAvailable() bool { return browserSocket() != "" }

// NewBrowserTool builds the facade. allowControl=false is the sub-agent shape:
// the same socket and browser, but only the read-only actions.
func NewBrowserTool(cwd string, allowControl bool) *BrowserTool {
	t := &BrowserTool{
		newTuios: func(ctx context.Context) (*tuiosctl.Client, error) {
			return tuiosctl.Dial(ctx, tuiosctl.FromEnv())
		},
		cwd: cwd, allowControl: allowControl,
		Control: NewControlGuard(cwd),
	}
	t.newClient = func() (*browserctl.Client, error) { return browserctl.Dial(t.socketPath()) }
	return t
}

// BrowserTool is the browser facade.
type BrowserTool struct {
	Control *ControlGuard
	// newClient resolves the control socket. Tests replace it with a client
	// backed by a stub transport, so no real Unix socket is needed.
	newClient    func() (*browserctl.Client, error)
	newTuios     func(context.Context) (*tuiosctl.Client, error)
	cwd          string
	allowControl bool
	discover     func(context.Context) browserDiscovery

	// lastTab is the last tab explicitly opened or selected by this tool. It
	// remains pinned even when Chromium changes its globally active tab.
	tabMu        sync.Mutex
	lastTab      string
	lastTargetID string
	boundSocket  string
	boundBrowser string
	boundSession string
}

func (t *BrowserTool) Name() string { return "browser" }

const browserToolUseHint = " Use this tool rather than launching `golder browse` or `kitty-browse` through bash: " +
	"the browser tool owns the pane identity, control socket and cleanup. " +
	"The default open uses a persistent project-shared profile and remembers logins; profile:<name> selects a separate project-scoped identity whose first use needs exact operator confirmation. " +
	"On macOS, a manual GUI Chromium login against that profile must use the status-reported profile, basic password store, mock keychain and browser proxy after the owned browser fully closes; otherwise cookies may exist but be unreadable. " +
	"If verification cannot be completed in the pane and the person explicitly requests a GUI login, inspect status before that narrow installed-Chromium fallback; never import a personal profile, overlap profile users, read or export cookies, or retry verification to bypass it. " +
	"Use status to inspect activity/pause state. On browser_paused, stop; do not retry, refresh or open another browser to bypass verification. " +
	"Only resume after the person explicitly continues and confirms the call, or uses Ctrl+G in the pane."

func (t *BrowserTool) useHint() string {
	if NativeBrowserLauncherAvailable() {
		return " In the desktop GUI, open starts the native browser host; the Browser icon opens its interactive Bitmap surface in the right drawer. The browser tool owns the control socket and cleanup. " +
			"The default open uses a persistent project-shared profile and remembers logins; profile:<name> selects a separate project-scoped identity whose first use needs exact operator confirmation. " +
			"Use status to inspect activity/pause state. On browser_paused, stop; do not retry or open another browser to bypass verification." + t.selectionHint()
	}
	return browserToolUseHint + t.selectionHint()
}

func (t *BrowserTool) selectionHint() string {
	if !t.allowControl {
		return " A tab argument observes that tab in the background and does not change what the person sees; ask the parent to select the main working tab when appropriate."
	}
	return " When starting or returning to the primary working tab, explicitly use tab_select with its tab ID so the person sees the working page in the browser pane or GUI. " +
		"A tab argument on snapshot, screenshot, click or other actions targets that tab without switching the visible page; keep brief background observations in the background. " +
		"This switches the browser tab, not the operating-system window. Never use tab_select to resume or bypass a paused browser."
}

func (t *BrowserTool) launchPolicy() string {
	if NativeBrowserLauncherAvailable() {
		return " Ambient proxy variables are ignored; the native desktop host currently requires the saved browser proxy to be off."
	}
	return " Ambient proxy variables are ignored; the person's saved /proxy browser setting is applied on launch."
}

func (t *BrowserTool) Description() string {
	if !t.available() {
		if !t.allowControl {
			return "List live workspace browsers and tabs. No browser is selected; ask the parent agent to attach one." +
				t.useHint()
		}
		return "No browser is selected. Call list to discover live instances, then attach using browserId. " +
			"Or call open with a URL to reuse an owned instance or start a controlled browser with the persistent project-shared profile. On macOS the approval gate decides a browser-only exception outside outer Seatbelt, retaining Chromium's own sandbox and a scrubbed environment." + t.launchPolicy() + " External control requires host_control:true and one-call authorization." +
			t.useHint()
	}
	if t.allowControl {
		return "Drive the running browser: snapshot the accessibility tree, " +
			"with control limited to owned controlled browsers by default; host_control:true requests one-call external authority. " +
			"click/fill/type/press by stable ref, navigate, wait, read console/network, " +
			"and screenshot. list discovers instances; attach selects a browserId; quit requests a graceful browser shutdown. " +
			"Pin a tab by its id from list; refs are per document. Prefer waiting for page readiness over repeated reloads. " +
			"A timed-out mutation may already have happened: inspect the current state and never automatically repeat an action with unknown execution state." +
			t.useHint()
	}
	return "Read the running browser: list tabs, snapshot the accessibility tree, " +
		"read url/text/html, console and network errors, and screenshot." +
		t.useHint()
}

// available reports whether a control socket is resolvable right now.
func (t *BrowserTool) available() bool { return t.socketPath() != "" }

func (t *BrowserTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionParallel
}

// browserIdleActions are what is advertised before a socket exists: open starts
// one, list reports what is there. Everything else would fail the same way.
var browserIdleActions = []string{"open", "list", "attach"}

const browserReadSchema = `{
  "type":"object","additionalProperties":false,"required":["action"],
  "properties":{
    "action":{"type":"string","enum":["list","snapshot","find","get","is","wait","dialog","console","errors","network","screenshot","status"]},
    "browser":{"type":"string","description":"Exact browser identity, pinned before review."},
    "page_url":{"type":"string","description":"Expected page URL, pinned before review; a changed page is refused."},
    "tab":{"type":"string","description":"Tab id from list; omit for the pinned tab, or a safe active tab if none is pinned."},
    "target_id":{"type":"string","description":"Stable target identity, pinned before review."},
    "interactive":{"type":"boolean","description":"For snapshot: only actionable nodes."},
    "compact":{"type":"boolean","description":"For snapshot: compact output."},
    "max_nodes":{"type":"integer","minimum":1,"description":"For snapshot: node cap; default 5000."},
    "depth":{"type":"integer","minimum":0,"description":"For snapshot: max depth."},
    "include_text":{"type":"boolean","description":"For snapshot: include page text."},
    "selector":{"type":"string","description":"CSS selector for a locator action."},
    "ref":{"type":"string","description":"Stable ref from snapshot, e.g. @e12."},
    "role":{"type":"string","description":"ARIA role for a locator."},
    "name":{"type":"string","description":"Accessible name for a locator."},
    "text":{"type":"string","description":"Visible text for a locator, or the text to fill/type."},
    "exact":{"type":"boolean","description":"For find/get: exact text match."},
    "kind":{"type":"string","enum":["title","url","text","html","count","value","box","attr"],"description":"For get."},
    "attr":{"type":"string","description":"For get kind=attr: attribute name."},
    "visible":{"type":"string","enum":["visible","hidden","enabled","checked"],"description":"For is/wait."},
    "state":{"type":"string","description":"For wait: visible|hidden|domcontentloaded|load."},
    "url_pattern":{"type":"string","description":"For wait: URL substring, glob or exact pattern."},
    "timeout_ms":{"type":"integer","minimum":1,"maximum":120000,"description":"Wait budget; default 30000."},
    "clear":{"type":"boolean","description":"For console/errors/network: clear the buffer after reading."},
    "format":{"type":"string","enum":["png"],"description":"For screenshot: png only."},
    "full_page":{"type":"boolean","description":"For screenshot: capture the whole page."},
    "out":{"type":"string","description":"For screenshot: path under the workspace; default .golder/artifacts/screenshots."}
  }
}`

const browserControlSchema = `{
  "type":"object","additionalProperties":false,"required":["action"],
  "properties":{
    "action":{"type":"string","enum":["open","list","attach","snapshot","find","get","is","wait","dialog","console","errors","network","screenshot","navigate","click","dblclick","hover","focus","check","uncheck","fill","type","select","press","insert","scroll","scroll_into_view","tab_new","tab_close","tab_select","dialog_accept","dialog_dismiss","status","pause","resume","quit"]},
    "host_control":{"type":"boolean","description":"Request one-call access to a browser not created by this agent. Attaching does not grant later control."},
    "new_instance":{"type":"boolean","description":"For open: create a fresh controlled browser rather than adopting an existing instance. Chromium permits only one live browser per persistent profile."},
    "profile":{"type":"string","pattern":"^[a-z][a-z0-9_-]{0,63}$","description":"For open only: use a separate named project-scoped profile. Omit for the persistent project-shared default; other names need exact first-use confirmation. Never a personal Chrome profile or filesystem path."},
    "page_url":{"type":"string","description":"Expected page URL, pinned before review; a changed page is refused."},
    "browser":{"type":"string","description":"For attach: browserId from list's instances[].info. Never a socket path."},
    "socket":{"type":"string","description":"Prepared target fact for quit; never a model-supplied capability."},
    "tab":{"type":"string","description":"Tab id from list; omit for the pinned tab, or a safe active tab if none is pinned."},
    "target_id":{"type":"string","description":"Stable target identity, pinned before review."},
    "interactive":{"type":"boolean","description":"For snapshot: only actionable nodes."},
    "compact":{"type":"boolean","description":"For snapshot: compact output."},
    "max_nodes":{"type":"integer","minimum":1,"description":"For snapshot: node cap; default 5000."},
    "depth":{"type":"integer","minimum":0,"description":"For snapshot: max depth."},
    "include_text":{"type":"boolean","description":"For snapshot: include page text."},
    "selector":{"type":"string","description":"CSS selector for a locator action."},
    "ref":{"type":"string","description":"Stable ref from snapshot, e.g. @e12."},
    "role":{"type":"string","description":"ARIA role for a locator."},
    "name":{"type":"string","description":"Accessible name for a locator."},
    "text":{"type":"string","description":"Visible text for a locator, or the text to fill/type."},
    "exact":{"type":"boolean","description":"For find/get: exact text match."},
    "kind":{"type":"string","enum":["title","url","text","html","count","value","box","attr"],"description":"For get."},
    "attr":{"type":"string","description":"For get kind=attr: attribute name."},
    "visible":{"type":"string","enum":["visible","hidden","enabled","checked"],"description":"For is/wait."},
    "state":{"type":"string","description":"For wait: visible|hidden|domcontentloaded|load."},
    "url_pattern":{"type":"string","description":"For wait: URL substring, glob or exact pattern."},
    "timeout_ms":{"type":"integer","minimum":1,"maximum":120000,"description":"Wait budget; default 30000."},
    "clear":{"type":"boolean","description":"For console/errors/network: clear the buffer after reading."},
    "format":{"type":"string","enum":["png"],"description":"For screenshot: png only."},
    "full_page":{"type":"boolean","description":"For screenshot: capture the whole page."},
    "out":{"type":"string","description":"For screenshot: path under the workspace; default .golder/artifacts/screenshots."},
    "url":{"type":"string","description":"For navigate: where to go."},
    "key":{"type":"string","description":"For press: Enter, Tab, ctrl+a, ArrowDown."},
    "value":{"type":"string","description":"For select: option value."},
    "delta_x":{"type":"number","description":"For scroll: horizontal pixels."},
    "delta_y":{"type":"number","description":"For scroll: vertical pixels."},
    "prompt_text":{"type":"string","description":"For dialog_accept: text to type into a prompt."},
    "new_tab":{"type":"boolean","description":"For tab_new: open a tab instead of selecting."}
  }
}`

type browserArgs struct {
	HostControl bool    `json:"host_control,omitempty"`
	NewInstance bool    `json:"new_instance,omitempty"`
	Profile     string  `json:"profile,omitempty"`
	PageURL     string  `json:"page_url,omitempty"`
	Action      string  `json:"action"`
	Browser     string  `json:"browser,omitempty"`
	Socket      string  `json:"socket,omitempty"`
	Tab         string  `json:"tab,omitempty"`
	TargetID    string  `json:"target_id,omitempty"`
	Interactive bool    `json:"interactive,omitempty"`
	Compact     bool    `json:"compact,omitempty"`
	MaxNodes    int     `json:"max_nodes,omitempty"`
	Depth       *int    `json:"depth,omitempty"`
	IncludeText bool    `json:"include_text,omitempty"`
	Selector    string  `json:"selector,omitempty"`
	Ref         string  `json:"ref,omitempty"`
	Role        string  `json:"role,omitempty"`
	Name        string  `json:"name,omitempty"`
	Text        string  `json:"text,omitempty"`
	Exact       bool    `json:"exact,omitempty"`
	Kind        string  `json:"kind,omitempty"`
	Attr        string  `json:"attr,omitempty"`
	Visible     string  `json:"visible,omitempty"`
	State       string  `json:"state,omitempty"`
	URLPattern  string  `json:"url_pattern,omitempty"`
	TimeoutMS   int     `json:"timeout_ms,omitempty"`
	Clear       bool    `json:"clear,omitempty"`
	Format      string  `json:"format,omitempty"`
	FullPage    bool    `json:"full_page,omitempty"`
	Out         string  `json:"out,omitempty"`
	URL         string  `json:"url,omitempty"`
	Key         string  `json:"key,omitempty"`
	Value       string  `json:"value,omitempty"`
	DeltaX      float64 `json:"delta_x,omitempty"`
	DeltaY      float64 `json:"delta_y,omitempty"`
	PromptText  string  `json:"prompt_text,omitempty"`
	NewTab      bool    `json:"new_tab,omitempty"`
}

func (t *BrowserTool) Schema() json.RawMessage {
	if !t.allowControl {
		if !t.available() {
			return trimActionEnum(browserReadSchema, []string{"list"})
		}
		return json.RawMessage(browserReadSchema)
	}
	if !t.available() {
		return trimActionEnum(browserControlSchema, browserIdleActions)
	}
	return json.RawMessage(browserControlSchema)
}

// ValidationSchema is the widest form: the registry compiles it once, and a call
// that becomes possible after workspace create must still validate.
func (t *BrowserTool) ValidationSchema() json.RawMessage {
	if !t.allowControl {
		return json.RawMessage(browserReadSchema)
	}
	return json.RawMessage(browserControlSchema)
}

// trimActionEnum narrows the action enum of a schema literal without keeping a
// second copy of it.
func trimActionEnum(schema string, allowed []string) json.RawMessage {
	var doc map[string]any
	if err := json.Unmarshal([]byte(schema), &doc); err != nil {
		return json.RawMessage(schema)
	}
	props, _ := doc["properties"].(map[string]any)
	action, _ := props["action"].(map[string]any)
	if action == nil {
		return json.RawMessage(schema)
	}
	action["enum"] = allowed
	out, err := json.Marshal(doc)
	if err != nil {
		return json.RawMessage(schema)
	}
	return out
}

// browserReadOnly reports whether an action only observes. It mirrors the
// capability table in kitty-browse's docs/action-api.md.
func browserReadOnly(action string) bool {
	switch action {
	case "list", "snapshot", "find", "get", "is", "wait", "dialog",
		"console", "errors", "network", "screenshot", "status":
		return true
	}
	return false
}

func (t *BrowserTool) Execute(ctx context.Context, callID string, raw json.RawMessage, _ agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
	if err := controlFloor(ctx, "browser", raw); err != nil {
		return browserError(err), nil
	}
	ctx = withControlGuard(ctx, t.Control)
	var a browserArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return browserError(err), nil
		}
	}
	if a.Action == "" {
		return browserError(errors.New("action is required")), nil
	}
	if !t.allowControl && !browserReadOnly(a.Action) {
		return browserError(errors.New("browser control actions are not available to sub-agents")), nil
	}
	if err := controlcap.BrowserURL(a.URL); err != nil {
		return browserError(err), nil
	}
	if t.Control == nil {
		return browserError(errors.New("browser control policy is not configured")), nil
	}
	if a.Profile != "" {
		if a.Action != "open" || !browserProfileName.MatchString(a.Profile) {
			return browserError(errors.New("profile is a validated name supported only by open")), nil
		}
		if err := t.authorizeProfile(ctx, callID, raw, a.Profile); err != nil {
			return browserError(err), nil
		}
	}
	if a.Action == "resume" && !agentcore.OperatorConfirmedFor(ctx, callID, t.Name(), raw) {
		return browserError(errors.New("resuming automation needs your exact confirmation; use /permissions ask, or press Ctrl+G in the browser pane")), nil
	}
	if a.Action == "screenshot" && (agentcore.SandboxRequestedFromContext(ctx) || (t.Control.Mode != nil && t.Control.Mode.Mode() == permissions.ReadOnly)) {
		return browserError(errors.New("screenshot writes cannot satisfy read-only/required OS containment")), nil
	}
	if !browserReadOnly(a.Action) {
		if t.Control.Mode != nil && t.Control.Mode.Mode() == permissions.ReadOnly {
			return browserError(errors.New("read-only mode cannot control browsers")), nil
		}
		if controlcap.Network("browser", raw) && !t.Control.network(ctx) {
			return browserError(errors.New("browser input/navigation requires reviewed network permission")), nil
		}
	}
	// open runs before the dial: its whole job is to create the socket the dial
	// would need.
	if a.Action == "open" {
		return t.open(ctx, a)
	}
	if a.Action == "attach" {
		for _, instance := range t.discovery(ctx).Instances {
			if instance.Info.BrowserID == a.Browser {
				if err := t.Control.checkBrowser(ctx, a.Browser, instance.Socket, a.HostControl); err != nil {
					return browserError(err), nil
				}
			}
		}
		return t.attach(ctx, a.Browser)
	}
	if a.Action == "list" {
		return t.listInstances(ctx), nil
	}
	selectedSocket := t.socketPath()
	if a.Action == "quit" && (a.Browser == "" || a.Socket == "" || a.Socket != selectedSocket) {
		return browserError(errors.New("quit requires the browser identity and socket pinned before review")), nil
	}
	c, err := t.newClient()
	if err != nil {
		// The dial's own error names an environment variable; inside a pane the
		// answer is a tool call, so say that instead.
		if t.socketPath() == "" {
			return browserError(errors.New(
				"no browser is selected; call list to discover live instances, then attach, or call open")), nil
		}
		return browserError(err), nil
	}
	info, err := c.Info(ctx)
	if err != nil {
		return browserError(fmt.Errorf("selected socket %q: %w", t.socketPath(), err)), nil
	}
	if err := t.checkProfileAccess(info); err != nil {
		return browserError(err), nil
	}
	if err := t.checkBinding(info); err != nil {
		return browserError(err), nil
	}
	if a.Browser != "" && a.Browser != info.BrowserID {
		return browserError(errors.New("browser identity changed after review; nothing was sent")), nil
	}
	if a.Action == "quit" && t.socketPath() != selectedSocket {
		return browserError(errors.New("selected browser socket changed after review; nothing was sent")), nil
	}
	if !browserReadOnly(a.Action) || agentcore.SandboxRequestedFromContext(ctx) {
		if err := t.Control.checkBrowser(ctx, info.BrowserID, selectedSocket, a.HostControl); err != nil {
			return browserError(err), nil
		}
	}

	switch a.Action {
	case "quit":
		if !info.HasCapability(browserctl.CapQuit) {
			return browserError(errors.New("browser does not advertise graceful quit; nothing was sent")), nil
		}
		timeout := browserCloseTimeout
		if a.TimeoutMS > 0 && time.Duration(a.TimeoutMS)*time.Millisecond < timeout {
			timeout = time.Duration(a.TimeoutMS) * time.Millisecond
		}
		quitCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		if err := c.Quit(quitCtx); err != nil {
			return browserActionError(a.Action, err), nil
		}
		return agentcore.AgentToolResult{
			Content: agentcore.ContentList{agentcore.NewTextContent(tuiosJSON(map[string]any{
				"ok": true, "browser": info.BrowserID, "quitting": true, "exit_confirmed": false,
				"hint": "Shutdown was accepted but exit is not yet confirmed; use list before reusing the profile.",
			}))},
			Details: map[string]any{"action": "quit", "browser": info.BrowserID, "socket": selectedSocket},
		}, nil
	case "status", "pause", "resume":
		result, meta, err := c.Action(ctx, "interaction."+a.Action, map[string]any{}, browserctl.Target{BrowserID: info.BrowserID}, 5*time.Second)
		if err != nil {
			return browserError(err), nil
		}
		payload := map[string]any{"ok": true, "result": json.RawMessage(result)}
		if a.Action == "status" {
			payload["runtime"] = t.browserRuntimeDiagnostics(info)
		}
		return agentcore.AgentToolResult{
			Content: agentcore.ContentList{agentcore.NewTextContent(tuiosJSON(payload))},
			Details: map[string]any{"action": a.Action, "browser": meta.BrowserID},
		}, nil
	}

	target, err := t.resolveTarget(info, a.Tab)
	if err != nil {
		return browserError(err), nil
	}
	if a.TargetID != "" && target.TargetID != a.TargetID {
		return browserError(errors.New("tab target changed after review; nothing was sent")), nil
	}
	var targetURL string
	for _, tab := range info.Tabs {
		if tab.ID == target.TabID {
			targetURL = tab.URL
			if tab.URL == "" {
				return browserError(errors.New("selected tab has no known URL; nothing was sent")), nil
			}
			if a.PageURL != "" && tab.URL != a.PageURL {
				return browserError(errors.New("page URL changed after review; nothing was sent")), nil
			}
			if err := controlcap.BrowserURL(tab.URL); err != nil {
				// Closing a known Chromium-internal target in our own browser is
				// cleanup, not navigation or permission to inspect that page.
				if a.Action != "tab_close" || strings.TrimSpace(a.Tab) == "" || !strings.HasPrefix(tab.URL, "chrome://") || !t.Control.ownsBrowser(ctx, info.BrowserID, t.socketPath()) {
					return browserError(err), nil
				}
			}
		}
	}
	if a.Action == "screenshot" {
		png, meta, err := c.ScreenshotTargetPage(ctx, target, a.FullPage)
		if err != nil {
			return browserError(err), nil
		}
		if meta.URL != targetURL {
			return browserError(errors.New("page URL changed after review; screenshot was not saved")), nil
		}
		if meta.URL == "" {
			return browserError(errors.New("screenshot response has no page URL; screenshot was not saved")), nil
		}
		if err := controlcap.BrowserURL(meta.URL); err != nil {
			return browserError(err), nil
		}
		if cfg, _, cfgErr := image.DecodeConfig(bytes.NewReader(png)); cfgErr == nil {
			if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > browserScreenshotMaxPixels {
				return browserError(fmt.Errorf(
					"screenshot is %dx%d (%d pixels), over the %d-pixel decode limit; use a viewport-sized capture",
					cfg.Width, cfg.Height, int64(cfg.Width)*int64(cfg.Height), browserScreenshotMaxPixels)), nil
			}
		}
		out, err := artifactPath(t.cwd, "screenshots", a.Out, "png")
		if err != nil {
			return browserError(err), nil
		}
		if err := os.WriteFile(out, png, 0o644); err != nil {
			return browserError(fmt.Errorf("write screenshot: %w", err)), nil
		}
		return agentcore.AgentToolResult{
			Content: agentcore.ContentList{agentcore.NewTextContent(tuiosJSON(map[string]any{
				"path": out, "bytes": len(png), "hint": "view_image this path to inspect the page",
			}))},
			Details: map[string]any{"path": out, "bytes": len(png), "tab": target.TabID},
		}, nil
	}
	action, params, err := browserActionParams(a)
	if err != nil {
		return browserError(err), nil
	}
	if a.Action == "tab_select" || a.Action == "tab_close" {
		params["tabId"] = target.TabID
	}
	timeout := time.Duration(a.TimeoutMS) * time.Millisecond
	if timeout <= 0 || timeout > browserMaxTimeout {
		timeout = browserMaxTimeout
	}
	result, meta, err := c.Action(ctx, action, params, target, timeout)
	if err != nil {
		return browserActionError(a.Action, err), nil
	}
	if a.Action == "tab_select" && (meta.BrowserID != info.BrowserID || meta.TabID != target.TabID || meta.TargetID != target.TargetID || meta.URL != targetURL) {
		return browserError(errors.New("selected tab identity or URL changed; call list and select it explicitly")), nil
	}
	if a.Action == "tab_new" && (meta.BrowserID != info.BrowserID || meta.TabID == "" || meta.TargetID == "" || meta.URL == "" || controlcap.BrowserURL(meta.URL) != nil) {
		return browserError(errors.New("new tab identity could not be confirmed; call list before another action")), nil
	}
	if a.Action == "tab_select" || a.Action == "tab_close" || a.Action == "tab_new" {
		t.tabMu.Lock()
		switch a.Action {
		case "tab_select":
			t.lastTab, t.lastTargetID = target.TabID, target.TargetID
		case "tab_close":
			if t.lastTab == target.TabID {
				t.lastTab, t.lastTargetID = "", ""
			}
		case "tab_new":
			t.lastTab, t.lastTargetID = meta.TabID, meta.TargetID
		}
		t.tabMu.Unlock()
	}
	details := map[string]any{"action": action, "tab": meta.TabID, "url": meta.URL}
	if a.Action == "tab_select" {
		// The GUI follows only a confirmed selection from this exact browser and
		// target. Older hosts lack the optional cross-process revision signal.
		details["browser"] = info.BrowserID
		details["target_id"] = target.TargetID
		details["socket"] = t.socketPath()
		var updated browserctl.Info
		if json.Unmarshal(result, &updated) == nil && updated.BrowserID == info.BrowserID && updated.ExplicitSelection != nil &&
			updated.ExplicitSelection.TabID == target.TabID && updated.ExplicitSelection.TargetID == target.TargetID {
			details["selection_revision"] = strconv.FormatUint(updated.ExplicitSelection.Revision, 10)
		}
	}
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(tuiosJSON(map[string]any{
			"ok": true, "action": action, "result": json.RawMessage(result),
			"url": meta.URL, "title": meta.Title,
		}))},
		Details: details,
	}, nil
}

// pinTab resolves an exact tab. Without one, use a safe active tab or the only
// safe tab; an internal active page must not silently redirect an action.
func pinTab(info browserctl.Info, want string) (browserctl.Target, error) {
	want = strings.TrimSpace(want)
	if want == "" {
		var candidate *browserctl.Tab
		count := 0
		for _, tab := range info.Tabs {
			if tab.ID == "" || tab.TargetID == "" || tab.URL == "" || controlcap.BrowserURL(tab.URL) != nil {
				continue
			}
			if tab.Active || tab.ID == info.ActiveTabID {
				return browserctl.Target{BrowserID: info.BrowserID, TabID: tab.ID, TargetID: tab.TargetID}, nil
			}
			count++
			copy := tab
			candidate = &copy
		}
		if count == 1 {
			return browserctl.Target{BrowserID: info.BrowserID, TabID: candidate.ID, TargetID: candidate.TargetID}, nil
		}
		if count > 1 {
			return browserctl.Target{}, errors.New("multiple safe tabs are open and no safe tab is active; pass tab from list")
		}
		return browserctl.Target{}, errors.New("the browser has no safe active tab; pass an allowed tab from list")
	}
	for _, tab := range info.Tabs {
		if tab.ID == want || tab.TargetID == want || strconv.Itoa(tab.Index) == want {
			if tab.ID == "" || tab.TargetID == "" {
				return browserctl.Target{}, errors.New("selected tab has no stable target identity")
			}
			return browserctl.Target{BrowserID: info.BrowserID, TabID: tab.ID, TargetID: tab.TargetID}, nil
		}
	}
	return browserctl.Target{}, fmt.Errorf("tab %q is not open; call list and use its id", want)
}

// resolveTarget preserves both IDs across calls. A disappeared or replaced
// pin is an error, never permission to silently act on a different target.
func (t *BrowserTool) resolveTarget(info browserctl.Info, want string) (browserctl.Target, error) {
	explicit := strings.TrimSpace(want) != ""
	t.tabMu.Lock()
	id, targetID := t.lastTab, t.lastTargetID
	t.tabMu.Unlock()
	if !explicit {
		want = id
	}
	target, err := pinTab(info, want)
	if err != nil {
		return browserctl.Target{}, err
	}
	if !explicit && targetID != "" && target.TargetID != targetID {
		return browserctl.Target{}, errors.New("pinned tab target was replaced; select a tab from list explicitly")
	}
	return target, nil
}

// selectOpenedTab binds startup to the requested page, or its unambiguous
// redirected target. It never retries a rejected activation, including pause.
func (t *BrowserTool) selectOpenedTab(ctx context.Context, socket, url string) error {
	c, err := browserctl.Dial(socket)
	if err != nil {
		return err
	}
	defer c.CloseIdleConnections()
	return t.selectOpenedTabWithClient(ctx, c, url)
}

func (t *BrowserTool) selectOpenedTabWithClient(ctx context.Context, c *browserctl.Client, url string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		info, err := c.Info(ctx)
		if err == nil {
			if err := t.checkBinding(info); err != nil {
				return err
			}
			if err := t.checkProfileAccess(info); err != nil {
				return err
			}
			if tab, ok := initialBrowserTab(info, url); ok {
				if err := activateBrowserTab(ctx, c, info, tab); err != nil {
					return err
				}
				t.tabMu.Lock()
				t.lastTab, t.lastTargetID = tab.ID, tab.TargetID
				t.tabMu.Unlock()
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("cannot identify the opened browser tab; call list and select it explicitly: %w", ctx.Err())
		case <-tick.C:
		}
	}
}

func initialBrowserTab(info browserctl.Info, rawURL string) (browserctl.Tab, bool) {
	var exact, fallback browserctl.Tab
	exactCount, fallbackCount := 0, 0
	for _, tab := range info.Tabs {
		if tab.ID == "" || tab.TargetID == "" || tab.URL == "" || controlcap.BrowserURL(tab.URL) != nil {
			continue
		}
		if sameBrowserURL(tab.URL, rawURL) {
			exact, exactCount = tab, exactCount+1
		}
		if tab.URL != "about:blank" || rawURL == "about:blank" {
			fallback, fallbackCount = tab, fallbackCount+1
		}
	}
	if exactCount == 1 {
		return exact, true
	}
	// The host's initially opened tab is index 1. A later restored tab must
	// never be mistaken for a redirect while the initial page is still blank.
	if exactCount == 0 && fallbackCount == 1 && fallback.Index == 1 {
		return fallback, true
	}
	return browserctl.Tab{}, false
}

// Normalize only the empty root path; other trailing slashes, queries, and
// fragments can identify genuinely different pages and must not be collapsed.
func sameBrowserURL(a, b string) bool {
	if a == b {
		return true
	}
	left, leftErr := url.Parse(a)
	right, rightErr := url.Parse(b)
	if leftErr != nil || rightErr != nil || left.Scheme == "" || right.Scheme == "" {
		return false
	}
	if left.Path == "" && left.Host != "" {
		left.Path = "/"
	}
	if right.Path == "" && right.Host != "" {
		right.Path = "/"
	}
	return left.String() == right.String()
}

func matchingBrowserTab(info browserctl.Info, rawURL, pinnedTab, pinnedTarget string) (browserctl.Tab, bool, error) {
	var match, preferred browserctl.Tab
	count := 0
	for _, tab := range info.Tabs {
		if !sameBrowserURL(tab.URL, rawURL) {
			continue
		}
		if tab.ID == "" || tab.TargetID == "" {
			return browserctl.Tab{}, false, errors.New("matching tab has no stable target identity")
		}
		if tab.ID == pinnedTab && pinnedTarget != "" && tab.TargetID != pinnedTarget {
			return browserctl.Tab{}, false, errors.New("pinned tab target was replaced; select a tab explicitly")
		}
		if tab.ID == pinnedTab && tab.TargetID == pinnedTarget {
			preferred = tab
		}
		match, count = tab, count+1
	}
	if preferred.ID != "" {
		return preferred, true, nil
	}
	if count > 1 {
		return browserctl.Tab{}, false, errors.New("multiple tabs show that URL; select the intended tab explicitly")
	}
	return match, count == 1, nil
}

func activateBrowserTab(ctx context.Context, c *browserctl.Client, info browserctl.Info, tab browserctl.Tab) error {
	if tab.Active || tab.ID == info.ActiveTabID {
		return nil
	}
	_, meta, err := c.Action(ctx, "tab.activate", map[string]any{"tabId": tab.ID},
		browserctl.Target{BrowserID: info.BrowserID, TabID: tab.ID, TargetID: tab.TargetID}, 5*time.Second)
	if err != nil {
		return err
	}
	if meta.BrowserID != info.BrowserID || meta.TabID != tab.ID || meta.TargetID != tab.TargetID || meta.URL != tab.URL {
		return errors.New("activated tab identity or URL changed; select it explicitly")
	}
	return nil
}

// browserActionParams maps the tool's flat arguments onto one browser
// action and its params. Unknown combinations fail before a round trip.
func browserActionParams(a browserArgs) (string, map[string]any, error) {
	locator := func() map[string]any {
		m := map[string]any{}
		if a.Ref != "" {
			m["ref"] = a.Ref
		}
		if a.Selector != "" {
			m["selector"] = a.Selector
		}
		if a.Role != "" {
			m["role"] = a.Role
		}
		if a.Name != "" {
			m["name"] = a.Name
		}
		// Visible text is a locator, not only the value of fill/type. Without
		// it, clicking a titled card ("Basic Line Chart") needed a ref first.
		if a.Text != "" {
			m["text"] = a.Text
		}
		return m
	}
	baseLocator := func() map[string]any {
		m := map[string]any{}
		if a.Ref != "" {
			m["ref"] = a.Ref
		}
		if a.Selector != "" {
			m["selector"] = a.Selector
		}
		if a.Role != "" {
			m["role"] = a.Role
		}
		if a.Name != "" {
			m["name"] = a.Name
		}
		return m
	}
	withText := func() map[string]any {
		m := baseLocator()
		if a.Text == "" {
			return nil
		}
		m["text"] = a.Text
		return m
	}
	switch a.Action {
	case "snapshot":
		p := map[string]any{}
		if a.Interactive {
			p["interactive"] = true
		}
		if a.Compact {
			p["compact"] = true
		}
		if a.MaxNodes > 0 {
			p["maxNodes"] = a.MaxNodes
		}
		if a.Depth != nil {
			p["depth"] = *a.Depth
		}
		if a.IncludeText {
			p["includeText"] = true
		}
		return "snapshot", p, nil
	case "find":
		p := locator()
		if len(p) == 0 {
			return "", nil, errors.New("find needs selector, ref, role, name or text")
		}
		if a.Exact {
			p["exact"] = true
		}
		return "find", p, nil
	case "get":
		if a.Kind == "" {
			return "", nil, errors.New("get needs kind")
		}
		p := map[string]any{"kind": a.Kind}
		for k, v := range locator() {
			p[k] = v
		}
		if a.Attr != "" {
			p["name"] = a.Attr
		}
		return "get", p, nil
	case "is":
		if a.Visible == "" {
			return "", nil, errors.New("is needs visible=visible|hidden|enabled|checked")
		}
		p := locator()
		if len(p) == 0 {
			return "", nil, errors.New("is needs a locator")
		}
		p["kind"] = a.Visible
		return "is", p, nil
	case "wait":
		p := locator()
		if a.URLPattern != "" {
			p["pattern"] = a.URLPattern
			return "wait.url", p, nil
		}
		if a.State == "domcontentloaded" || a.State == "load" {
			return "wait.loadState", map[string]any{"state": a.State}, nil
		}
		if a.State != "" {
			p["state"] = a.State
		}
		if a.TimeoutMS > 0 {
			p["timeoutMs"] = a.TimeoutMS
		}
		return "wait", p, nil
	case "dialog":
		return "dialog.status", map[string]any{}, nil
	case "console", "errors":
		p := map[string]any{}
		if a.Clear {
			p["clear"] = true
		}
		return a.Action, p, nil
	case "network":
		p := map[string]any{}
		if a.Clear {
			p["clear"] = true
		}
		return "network.requests", p, nil
	case "navigate":
		if a.URL == "" {
			return "", nil, errors.New("navigate needs url")
		}
		if strings.HasPrefix(strings.ToLower(a.URL), "file:") {
			return "", nil, errors.New("navigate refuses file:// URLs")
		}
		return "navigate", map[string]any{"url": a.URL}, nil
	case "click", "dblclick", "hover", "focus", "check", "uncheck", "scroll_into_view":
		p := locator()
		if len(p) == 0 {
			return "", nil, errors.New(a.Action + " needs selector, ref, role, name or text")
		}
		action := a.Action
		if a.Action == "scroll_into_view" {
			action = "scrollintoview"
		}
		return action, p, nil
	case "fill", "type":
		p := withText()
		if p == nil {
			return "", nil, errors.New(a.Action + " needs text")
		}
		// text is the value here, so it cannot also identify the element.
		if len(baseLocator()) == 0 {
			return "", nil, errors.New(a.Action + " needs a locator (ref, selector, role or name)")
		}
		return a.Action, p, nil
	case "select":
		p := locator()
		if a.Value == "" {
			return "", nil, errors.New("select needs value")
		}
		p["value"] = a.Value
		return "select", p, nil
	case "press":
		if a.Key == "" {
			return "", nil, errors.New("press needs key")
		}
		return "press", map[string]any{"key": a.Key}, nil
	case "insert":
		if a.Text == "" {
			return "", nil, errors.New("insert needs text")
		}
		return "keyboard.insert", map[string]any{"text": a.Text}, nil
	case "scroll":
		p := map[string]any{}
		if a.DeltaX != 0 {
			p["deltaX"] = a.DeltaX
		}
		if a.DeltaY != 0 {
			p["deltaY"] = a.DeltaY
		}
		return "scroll", p, nil
	case "tab_new":
		p := map[string]any{}
		if url := strings.TrimSpace(a.URL); url != "" {
			p["url"] = url
		}
		return "tab.new", p, nil
	case "tab_close":
		id := strings.TrimSpace(a.Tab)
		if id == "" {
			return "", nil, errors.New("tab_close needs tab: the id from list")
		}
		return "tab.close", map[string]any{"tabId": id}, nil
	case "tab_select":
		// The action is tab.activate; tab.select does not exist, so every call
		// used to fail with unknown_action and no id was sent.
		id := strings.TrimSpace(a.Tab)
		if id == "" {
			return "", nil, errors.New("tab_select needs tab: the id from list")
		}
		return "tab.activate", map[string]any{"tabId": id}, nil
	case "dialog_accept":
		p := map[string]any{}
		if a.PromptText != "" {
			p["promptText"] = a.PromptText
		}
		return "dialog.accept", p, nil
	case "dialog_dismiss":
		return "dialog.dismiss", map[string]any{}, nil
	}
	return "", nil, fmt.Errorf("unknown action %q", a.Action)
}

// open starts a browser in a tuios pane and adopts its control socket, so a
// session begins with one call instead of three shell commands. It is
// idempotent: with a browser already running it reports the existing socket.
func (t *BrowserTool) open(ctx context.Context, a browserArgs) (agentcore.AgentToolResult, error) {
	d := t.discovery(ctx)
	if a.NewInstance {
		d.Instances = nil
		d.Truncated = false
	}
	// The project default is deliberately shared and may be adopted by another
	// agent session. Discovery alone is not authority for any other named profile.
	filtered := d.Instances[:0]
	effectiveProfile := effectiveBrowserProfileName(a.Profile)
	for _, instance := range d.Instances {
		if a.Browser != "" && instance.Info.BrowserID != a.Browser {
			continue
		}
		if t.checkProfileAccess(instance.Info) != nil {
			continue
		}
		t.Control.mu.Lock()
		owned, ok := t.Control.browsers[instance.Info.BrowserID]
		t.Control.mu.Unlock()
		matches := instance.Info.LoginProfile == browserLoginProfile(t.cwd, effectiveProfile)
		if matches && t.Control.profileAllowed(effectiveProfile) {
			proxyMode := browserProxyModeUnknown
			if ok && owned.proxyMode != "" {
				proxyMode = owned.proxyMode
			}
			t.Control.mu.Lock()
			t.Control.browsers[instance.Info.BrowserID] = controlledBrowser{socket: canonicalBrowserSocket(instance.Socket), profile: effectiveProfile, proxyMode: proxyMode, browserRuntime: true, policy: controlPolicyStamp()}
			t.Control.mu.Unlock()
		} else if a.Profile != "" && (!ok || owned.profile != effectiveProfile) {
			continue
		}
		if t.Control.checkBrowser(ctx, instance.Info.BrowserID, instance.Socket, a.HostControl) == nil {
			filtered = append(filtered, instance)
		}
	}
	d.Instances = filtered
	if a.Browser != "" && len(filtered) == 0 {
		return browserError(errors.New("the reviewed browser is unavailable or not authorized")), nil
	}
	var selected *browserInstance
	for i := range d.Instances {
		if d.Instances[i].Selected {
			selected = &d.Instances[i]
			break
		}
	}
	if selected == nil {
		if len(d.Instances) > 1 || d.Truncated {
			res := t.listInstancesResult(d)
			res.IsError = true
			res.Content = append(res.Content, agentcore.NewTextContent("No unambiguous live selection. Call attach with a browserId from these instances; no browser was switched or launched."))
			return res, nil
		}
		if len(d.Instances) == 1 {
			selected = &d.Instances[0]
		}
	}
	if selected != nil {
		if selected.Selected {
			if err := t.checkBinding(selected.Info); err != nil {
				return browserError(err), nil
			}
		}
		t.bind(*selected)
		socket := selected.Socket
		url := strings.TrimSpace(a.URL)
		if url != "" {
			if err := t.showURL(ctx, socket, url); err != nil {
				return browserError(err), nil
			}
		}
		return agentcore.AgentToolResult{
			Content: agentcore.ContentList{agentcore.NewTextContent(tuiosJSON(map[string]any{
				"socket": socket, "started": false, "adopted": true,
			}))},
			Details: map[string]any{"socket": socket, "started": false, "adopted": true},
		}, nil
	}
	if !tuiosctl.Available() && !NativeBrowserLauncherAvailable() {
		return browserError(errors.New(
			"no browser is running and none can be opened here: this process has neither a native browser host nor a tuios pane")), nil
	}
	url := strings.TrimSpace(a.URL)
	if url == "" {
		url = "http://localhost:3000"
	}
	name := "browser"
	if a.NewInstance && t.Control.conversation() != "" && len(t.discovery(ctx).Instances) > 0 {
		name += "-" + rand.Text()[:8]
	}
	if NativeBrowserLauncherAvailable() {
		socket, err := startNativeBrowserWithProfile(ctx, t.cwd, name, url, a.Profile)
		if err != nil {
			return browserError(err), nil
		}
		probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		discovered := discoverBrowsers(probeCtx, t.cwd, socket)
		cancel()
		for _, instance := range discovered.Instances {
			if instance.Socket == canonicalBrowserSocket(socket) {
				t.bind(instance)
				break
			}
		}
		if err := t.selectOpenedTab(ctx, socket, url); err != nil {
			return browserError(err), nil
		}
		return agentcore.AgentToolResult{
			Content: agentcore.ContentList{agentcore.NewTextContent(tuiosJSON(map[string]any{
				"socket": socket, "url": url, "started": true, "surface": "native",
			}))},
			Details: map[string]any{"socket": socket, "started": true, "surface": "native"},
		}, nil
	}
	socket, window, err := startBrowserPaneWithProfile(ctx, t.cwd, name, url, a.Profile, t.newTuios)
	if err != nil {
		return browserError(err), nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	discovered := discoverBrowsers(probeCtx, t.cwd, socket)
	cancel()
	for _, instance := range discovered.Instances {
		if instance.Socket == canonicalBrowserSocket(socket) {
			t.bind(instance)
			break
		}
	}
	if err := t.selectOpenedTab(ctx, socket, url); err != nil {
		return browserError(err), nil
	}
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(tuiosJSON(map[string]any{
			"socket": socket, "window_id": window, "url": url, "started": true,
		}))},
		Details: map[string]any{"socket": socket, "window_id": window, "started": true},
	}, nil
}

// showURL makes url the page the person sees in an already-running browser:
// select the tab that already shows it, or navigate the selected tab there.
// Without this, open with a live socket reported success and changed nothing.
func (t *BrowserTool) showURL(ctx context.Context, socket, url string) error {
	if err := controlcap.BrowserURL(url); err != nil {
		return err
	}
	c, err := browserctl.Dial(socket)
	if err != nil {
		return err
	}
	defer c.CloseIdleConnections()
	info, err := c.Info(ctx)
	if err != nil {
		return err
	}
	if err := t.checkProfileAccess(info); err != nil {
		return err
	}
	if err := t.checkBinding(info); err != nil {
		return err
	}
	t.tabMu.Lock()
	pinnedTab, pinnedTarget := t.lastTab, t.lastTargetID
	t.tabMu.Unlock()
	tab, found, err := matchingBrowserTab(info, url, pinnedTab, pinnedTarget)
	if err != nil {
		return err
	}
	if found {
		if err := activateBrowserTab(ctx, c, info, tab); err != nil {
			return err
		}
		t.tabMu.Lock()
		t.lastTab, t.lastTargetID = tab.ID, tab.TargetID
		t.tabMu.Unlock()
		return nil
	}
	target, err := t.resolveTarget(info, "")
	if err != nil {
		return err
	}
	for _, tab := range info.Tabs {
		if tab.ID == target.TabID {
			if tab.URL == "" {
				return errors.New("selected tab has no known URL; navigation was not sent")
			}
			if err := controlcap.BrowserURL(tab.URL); err != nil {
				return err
			}
		}
	}
	if _, _, err := c.Action(ctx, "navigate", map[string]any{"url": url}, target, 60*time.Second); err != nil {
		return err
	}
	t.tabMu.Lock()
	t.lastTab, t.lastTargetID = target.TabID, target.TargetID
	t.tabMu.Unlock()
	return nil
}

// browserActionError does not turn a transport or host retry hint into
// permission to replay a mutation that may already have reached the page.
func browserActionError(action string, err error) agentcore.AgentToolResult {
	if browserReadOnly(action) || browserctl.IsConnectionRefused(err) {
		return browserError(err)
	}
	var be *browserctl.Error
	if errors.As(err, &be) {
		if be.Code == "browser_paused" {
			return browserError(err)
		}
		copy := *be
		copy.Retryable = false
		err = &copy
	}
	result := browserError(err)
	hint := "The action may have been dispatched; inspect the current page state and do not automatically repeat an action whose execution is unknown."
	if action == "quit" {
		hint = "The quit request may have been dispatched; call list to inspect the browser and do not automatically repeat a quit whose outcome is unknown."
	}
	result.Content = append(result.Content, agentcore.NewTextContent(hint))
	return result
}

func browserError(err error) agentcore.AgentToolResult {
	if browserctl.IsConnectionRefused(err) {
		return errorResult("browser: the selected control socket is unavailable; other browser panes may still be running. Call list to discover live instances, then attach using a browserId. No action was retried on another browser. Cause: " + err.Error())
	}
	var be *browserctl.Error
	if errors.As(err, &be) {
		msg := "browser: " + be.Code + ": " + be.Message
		if be.Retryable {
			msg += " (retryable)"
		}
		if len(be.Details) > 0 && string(be.Details) != "null" {
			msg += " details=" + string(be.Details)
		}
		if be.Code == "stale_ref" {
			msg += "; call snapshot again and use the new ref"
		}
		return errorResult(msg)
	}
	return errorResult("browser: " + err.Error())
}

// artifactPath resolves where a tool writes a picture. A relative path is under
// the workspace; an absolute one must still be inside it. With no out, a
// timestamped name lands under .golder/artifacts/<kind>/.
func artifactPath(root, kind, out, ext string) (string, error) {
	if strings.TrimSpace(root) == "" {
		var err error
		if root, err = os.Getwd(); err != nil {
			return "", fmt.Errorf("resolve workspace: %w", err)
		}
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	if ext == "" {
		ext = "png"
	}
	target := strings.TrimSpace(out)
	if target == "" {
		target = filepath.Join(root, ".golder", "artifacts", kind,
			fmt.Sprintf("%s-%d.%s", kind, time.Now().UnixNano(), ext))
	} else if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	target = filepath.Clean(target)
	if !insidePath(root, target) {
		return "", fmt.Errorf("%s must stay inside the workspace (%s)", target, root)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", fmt.Errorf("create artifact directory: %w", err)
	}
	return target, nil
}

// insidePath reports whether target is root or below it, after cleaning.
func insidePath(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}
