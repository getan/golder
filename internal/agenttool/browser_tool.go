package agenttool

// The browser tool drives one running kitty-browse through its typed control
// socket. It is not a second browser: nothing here launches Chromium or speaks
// CDP. The browser owns the page; this only asks for one action at a time, with
// the tab pinned to the ids /v1/info reported.
//
// Read-only actions are safe to run alone: snapshot, find, get, is, wait,
// dialog.status, console, errors, network, screenshot. Control actions put
// input into the page and are left to the reviewer. eval and upload are not
// part of the schema at all.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/browserctl"
)

const browserMaxTimeout = 120 * time.Second

// BrowserAvailable reports whether this process should advertise the tool.
func BrowserAvailable() bool { return browserctl.SocketFromEnv() != "" }

// NewBrowserTool builds the facade. allowControl=false is the sub-agent shape:
// the same socket and browser, but only the read-only actions.
func NewBrowserTool(cwd string, allowControl bool) *BrowserTool {
	return &BrowserTool{
		newClient: func() (*browserctl.Client, error) {
			return browserctl.Dial(browserctl.SocketFromEnv())
		},
		cwd: cwd, allowControl: allowControl,
	}
}

// BrowserTool is the browser facade.
type BrowserTool struct {
	// newClient resolves the control socket. Tests replace it with a client
	// backed by a stub transport, so no real Unix socket is needed.
	newClient    func() (*browserctl.Client, error)
	cwd          string
	allowControl bool
}

func (t *BrowserTool) Name() string { return "browser" }

func (t *BrowserTool) Description() string {
	if t.allowControl {
		return "Drive the running kitty-browse pane: snapshot the accessibility tree, " +
			"click/fill/type/press by stable ref, navigate, wait, read console/network, " +
			"and screenshot. Pin a tab by its id from list; refs are per document."
	}
	return "Read the running kitty-browse pane: list tabs, snapshot the accessibility tree, " +
		"read url/text/html, console and network errors, and screenshot."
}

func (t *BrowserTool) ExecutionMode() agentcore.ToolExecutionMode {
	return agentcore.ToolExecutionParallel
}

const browserReadSchema = `{
  "type":"object","additionalProperties":false,"required":["action"],
  "properties":{
    "action":{"type":"string","enum":["list","snapshot","find","get","is","wait","dialog","console","errors","network","screenshot"]},
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
    "limit":{"type":"integer","minimum":1,"description":"For console/errors/network: last N entries."},
    "clear":{"type":"boolean","description":"For console/errors/network: clear the buffer after reading."},
    "format":{"type":"string","enum":["png"],"description":"For screenshot: png only."},
    "full_page":{"type":"boolean","description":"For screenshot: capture the whole page."},
    "out":{"type":"string","description":"For screenshot: path under the workspace; default .golder/artifacts/screenshots."}
  }
}`

const browserControlSchema = `{
  "type":"object","additionalProperties":false,"required":["action"],
  "properties":{
    "action":{"type":"string","enum":["list","snapshot","find","get","is","wait","dialog","console","errors","network","screenshot","navigate","click","dblclick","hover","focus","check","uncheck","fill","type","select","press","insert","scroll","scroll_into_view","tab_new","tab_close","tab_select","dialog_accept","dialog_dismiss"]},
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
    "limit":{"type":"integer","minimum":1,"description":"For console/errors/network: last N entries."},
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
	Action      string  `json:"action"`
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
	Limit       int     `json:"limit,omitempty"`
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
		return json.RawMessage(browserReadSchema)
	}
	return json.RawMessage(browserControlSchema)
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
	c, err := t.newClient()
	if err != nil {
		return browserError(err), nil
	}

	switch a.Action {
	case "list":
		info, err := c.Info(ctx)
		if err != nil {
			return browserError(err), nil
		}
		type tabView struct {
			ID     string `json:"id"`
			URL    string `json:"url"`
			Title  string `json:"title"`
			Index  int    `json:"index"`
			Active bool   `json:"active"`
		}
		tabs := make([]tabView, 0, len(info.Tabs))
		for _, tab := range info.Tabs {
			tabs = append(tabs, tabView{ID: tab.ID, URL: tab.URL, Title: tab.Title, Index: tab.Index, Active: tab.Active})
		}
		return agentcore.AgentToolResult{
			Content: agentcore.ContentList{agentcore.NewTextContent(tuiosJSON(map[string]any{
				"browser": info.BrowserID, "backend": info.Backend, "tabs": tabs,
			}))},
			Details: map[string]any{"browser": info.BrowserID, "tabs": len(tabs)},
		}, nil
	case "screenshot":
		png, err := c.Screenshot(ctx, a.FullPage)
		if err != nil {
			return browserError(err), nil
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

	info, err := c.Info(ctx)
	if err != nil {
		return browserError(err), nil
	}
	target, err := pinTab(info, a.Tab)
	if err != nil {
		return browserError(err), nil
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

// browserActionParams maps the tool's flat arguments onto one kitty-browse
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
		return m
	}
	withText := func() map[string]any {
		m := locator()
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
			return "", nil, errors.New("find needs selector, ref, role or name")
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
		if a.Limit > 0 {
			p["limit"] = a.Limit
		}
		if a.Clear {
			p["clear"] = true
		}
		return a.Action, p, nil
	case "network":
		p := map[string]any{}
		if a.Limit > 0 {
			p["limit"] = a.Limit
		}
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
			return "", nil, errors.New(a.Action + " needs selector, ref, role or name")
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
		if _, ok := p["ref"]; !ok {
			if _, ok := p["selector"]; !ok {
				return "", nil, errors.New(a.Action + " needs a locator")
			}
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
		return "tab.new", map[string]any{}, nil
	case "tab_close":
		return "tab.close", map[string]any{}, nil
	case "tab_select":
		return "tab.select", map[string]any{}, nil
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

func browserError(err error) agentcore.AgentToolResult {
	if browserctl.IsConnectionRefused(err) {
		return errorResult("browser: the control socket is gone; the kitty-browse pane probably exited. Relaunch it, or use websearch/webfetch instead.")
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
