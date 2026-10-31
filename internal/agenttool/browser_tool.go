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
	"encoding/json"
	"errors"
	"fmt"
	"image"
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

	// lastTab remembers the tab open created. kitty-browse keeps a blank anchor
	// target in the background and can report it as active, so "no tab given"
	// must not mean "whatever the browser says is active" right after open.
	tabMu        sync.Mutex
	lastTab      string
	boundSocket  string
	boundBrowser string
}

func (t *BrowserTool) Name() string { return "browser" }

const browserToolUseHint = " Use this tool rather than launching `golder browse` or `kitty-browse` through bash: " +
	"the browser tool owns the pane identity, control socket and cleanup."

func (t *BrowserTool) Description() string {
	if !t.available() {
		if !t.allowControl {
			return "List live workspace browsers and tabs. No browser is selected; ask the parent agent to attach one." +
				browserToolUseHint
		}
		return "No browser is selected. Call list to discover live instances, then attach using browserId. " +
			"Or call open with a URL to reuse an owned instance or start a controlled browser with a fresh profile. On macOS the approval gate decides a browser-only exception outside outer Seatbelt, retaining Chromium's own sandbox and a scrubbed environment. Ambient proxy variables are ignored; the person's saved /proxy browser setting is applied on launch. External control requires host_control:true and one-call authorization." +
			browserToolUseHint
	}
	if t.allowControl {
		return "Drive the running browser pane: snapshot the accessibility tree, " +
			"with control limited to owned controlled browsers by default; host_control:true requests one-call external authority. " +
			"click/fill/type/press by stable ref, navigate, wait, read console/network, " +
			"and screenshot. list discovers instances; attach selects a browserId. " +
			"Pin a tab by its id from list; refs are per document." +
			browserToolUseHint
	}
	return "Read the running browser pane: list tabs, snapshot the accessibility tree, " +
		"read url/text/html, console and network errors, and screenshot." +
		browserToolUseHint
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
    "action":{"type":"string","enum":["list","snapshot","find","get","is","wait","dialog","console","errors","network","screenshot"]},
    "browser":{"type":"string","description":"Exact browser identity, pinned before review."},
    "page_url":{"type":"string","description":"Expected page URL, pinned before review; a changed page is refused."},
    "tab":{"type":"string","description":"Tab id from list; omit for the active tab."},
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
    "action":{"type":"string","enum":["open","list","attach","snapshot","find","get","is","wait","dialog","console","errors","network","screenshot","navigate","click","dblclick","hover","focus","check","uncheck","fill","type","select","press","insert","scroll","scroll_into_view","tab_new","tab_close","tab_select","dialog_accept","dialog_dismiss"]},
    "host_control":{"type":"boolean","description":"Request one-call access to a browser not created by this agent. Attaching does not grant later control."},
    "new_instance":{"type":"boolean","description":"For open: create a fresh controlled browser rather than adopting an existing instance."},
    "page_url":{"type":"string","description":"Expected page URL, pinned before review; a changed page is refused."},
    "browser":{"type":"string","description":"For attach: browserId from list's instances[].info. Never a socket path."},
    "tab":{"type":"string","description":"Tab id from list; omit for the active tab."},
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
	PageURL     string  `json:"page_url,omitempty"`
	Action      string  `json:"action"`
	Browser     string  `json:"browser,omitempty"`
	Tab         string  `json:"tab,omitempty"`
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
		"console", "errors", "network", "screenshot":
		return true
	}
	return false
}

func (t *BrowserTool) Execute(ctx context.Context, _ string, raw json.RawMessage, _ agentcore.ToolUpdateFunc) (agentcore.AgentToolResult, error) {
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
	if err := t.checkBinding(info); err != nil {
		return browserError(err), nil
	}
	if a.Browser != "" && a.Browser != info.BrowserID {
		return browserError(errors.New("browser identity changed after review; nothing was sent")), nil
	}
	if a.Action == "screenshot" {
		for _, tab := range info.Tabs {
			if err := controlcap.BrowserURL(tab.URL); err != nil {
				return browserError(err), nil
			}
		}
	}
	if !browserReadOnly(a.Action) || agentcore.SandboxRequestedFromContext(ctx) {
		if err := t.Control.checkBrowser(ctx, info.BrowserID, t.socketPath(), a.HostControl); err != nil {
			return browserError(err), nil
		}
	}

	switch a.Action {
	case "screenshot":
		png, err := c.Screenshot(ctx, a.FullPage)
		if err != nil {
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
			Details: map[string]any{"path": out, "bytes": len(png)},
		}, nil
	}

	target, err := pinTab(info, t.tabChoice(a.Tab, info))
	if err != nil {
		return browserError(err), nil
	}
	for _, tab := range info.Tabs {
		if tab.ID == target.TabID {
			if a.PageURL != "" && tab.URL != a.PageURL {
				return browserError(errors.New("page URL changed after review; nothing was sent")), nil
			}
			if err := controlcap.BrowserURL(tab.URL); err != nil {
				return browserError(err), nil
			}
		}
	}
	action, params, err := browserActionParams(a)
	if err != nil {
		return browserError(err), nil
	}
	timeout := time.Duration(a.TimeoutMS) * time.Millisecond
	if timeout <= 0 || timeout > browserMaxTimeout {
		timeout = browserMaxTimeout
	}
	result, meta, err := c.Action(ctx, action, params, target, timeout)
	if err != nil {
		return browserError(err), nil
	}
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(tuiosJSON(map[string]any{
			"ok": true, "action": action, "result": json.RawMessage(result),
			"url": meta.URL, "title": meta.Title,
		}))},
		Details: map[string]any{"action": action, "tab": meta.TabID, "url": meta.URL},
	}, nil
}

// pinTab resolves the tab to act on. No tab means the browser's active tab, as
// reported by the same info call, so the ids always agree with each other.
func pinTab(info browserctl.Info, want string) (browserctl.Target, error) {
	want = strings.TrimSpace(want)
	if want == "" {
		for _, tab := range info.Tabs {
			if tab.Active || tab.ID == info.ActiveTabID {
				return browserctl.Target{BrowserID: info.BrowserID, TabID: tab.ID, TargetID: tab.TargetID}, nil
			}
		}
		if len(info.Tabs) == 1 {
			tab := info.Tabs[0]
			return browserctl.Target{BrowserID: info.BrowserID, TabID: tab.ID, TargetID: tab.TargetID}, nil
		}
		return browserctl.Target{}, errors.New("the browser reported no active tab; pass tab from list")
	}
	for _, tab := range info.Tabs {
		if tab.ID == want || tab.TargetID == want {
			return browserctl.Target{BrowserID: info.BrowserID, TabID: tab.ID, TargetID: tab.TargetID}, nil
		}
		if strconv.Itoa(tab.Index) == want {
			return browserctl.Target{BrowserID: info.BrowserID, TabID: tab.ID, TargetID: tab.TargetID}, nil
		}
	}
	return browserctl.Target{}, fmt.Errorf("tab %q is not open; call list and use its id", want)
}

// tabChoice prefers an explicit tab, then the one open created, then the
// browser's own active tab.
func (t *BrowserTool) tabChoice(want string, info browserctl.Info) string {
	if strings.TrimSpace(want) != "" {
		return want
	}
	t.tabMu.Lock()
	id := t.lastTab
	t.tabMu.Unlock()
	for _, tab := range info.Tabs {
		if tab.ID == id {
			return id
		}
	}
	return ""
}

// selectOpenedTab records and activates the tab that now shows url. Without the
// activation the pane keeps showing whichever internal target Chromium opened
// last — usually a blank page that never produces a frame, which is why the
// first version of open left a black pane behind.
func (t *BrowserTool) selectOpenedTab(socket, url string) {
	c, err := browserctl.Dial(socket)
	if err != nil {
		return
	}
	want := strings.TrimSuffix(url, "/")
	// The socket appears before the tab does, so give it a moment.
	for i := 0; i < 15; i++ {
		info, err := c.Info(context.Background())
		if err == nil {
			for _, tab := range info.Tabs {
				if strings.TrimSuffix(tab.URL, "/") == want {
					t.tabMu.Lock()
					t.lastTab = tab.ID
					t.tabMu.Unlock()
					_, _, _ = c.Action(context.Background(), "tab.activate",
						map[string]any{"tabId": tab.ID},
						browserctl.Target{BrowserID: info.BrowserID}, 5*time.Second)
					return
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
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
	// Discovery is not authority. Never auto-adopt another session's profile.
	filtered := d.Instances[:0]
	for _, instance := range d.Instances {
		if a.Browser != "" && instance.Info.BrowserID != a.Browser {
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
	if !tuiosctl.Available() {
		return browserError(errors.New(
			"no browser is running and none can be opened here: a browser pane draws into a terminal, " +
				"so it needs a tuios pane. Start golder inside one; open starts the browser from there")), nil
	}
	url := strings.TrimSpace(a.URL)
	if url == "" {
		url = "http://localhost:3000"
	}
	socket, window, err := startBrowserPane(ctx, t.cwd, "browser", url, t.newTuios)
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
	t.selectOpenedTab(socket, url)
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
	info, err := c.Info(ctx)
	if err != nil {
		return err
	}
	if err := t.checkBinding(info); err != nil {
		return err
	}
	want := strings.TrimSuffix(url, "/")
	for _, tab := range info.Tabs {
		if strings.TrimSuffix(tab.URL, "/") != want {
			continue
		}
		t.tabMu.Lock()
		t.lastTab = tab.ID
		t.tabMu.Unlock()
		_, _, err := c.Action(ctx, "tab.activate", map[string]any{"tabId": tab.ID},
			browserctl.Target{BrowserID: info.BrowserID}, 5*time.Second)
		return err
	}
	target, err := pinTab(info, t.tabChoice("", info))
	if err != nil {
		return err
	}
	if _, _, err := c.Action(ctx, "navigate", map[string]any{"url": url}, target, 60*time.Second); err != nil {
		return err
	}
	t.tabMu.Lock()
	t.lastTab = target.TabID
	t.tabMu.Unlock()
	return nil
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
