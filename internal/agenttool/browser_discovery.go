package agenttool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/browserctl"
)

const maxBrowserCandidates = 32

type browserInstance struct {
	Socket   string          `json:"socket"`
	Selected bool            `json:"selected"`
	Info     browserctl.Info `json:"info"`
}

type browserDiscoveryFailure struct {
	Socket string `json:"socket"`
	Error  string `json:"error"`
}

type browserDiscovery struct {
	Instances []browserInstance         `json:"instances"`
	Failures  []browserDiscoveryFailure `json:"unavailable,omitempty"`
	Truncated bool                      `json:"truncated,omitempty"`
}

func canonicalBrowserSocket(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

// Discover only two shallow workspace directories, not profile directories or
// arbitrary machine-wide sockets. The explicitly configured endpoint is also
// included (it may deliberately live outside the workspace).
func browserCandidatePaths(cwd, selected string) ([]string, bool) {
	paths := make([]string, 0, maxBrowserCandidates)
	seen := map[string]bool{}
	truncated := false
	add := func(path string) {
		abs := canonicalBrowserSocket(path)
		if !seen[abs] {
			seen[abs] = true
			paths = append(paths, abs)
		}
	}
	if selected != "" {
		add(selected)
	}
	root, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return paths, false
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return paths, false
	}
	for _, dir := range []string{filepath.Join(root, ".golder", "browser"), filepath.Join(root, ".golder")} {
		// A workspace entry must not redirect discovery into another directory.
		real, err := filepath.EvalSymlinks(dir)
		if err != nil || !insidePath(root, real) {
			continue
		}
		f, err := os.Open(real)
		if err != nil {
			continue
		}
		// Bound filesystem work as well as probes, even in a huge directory.
		entries, _ := f.ReadDir(256)
		_ = f.Close()
		if len(entries) == 256 {
			truncated = true
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".sock") || entry.Type()&os.ModeSocket == 0 {
				continue // never follow a symlink or probe a regular file
			}
			if len(paths) == maxBrowserCandidates {
				return paths, true
			}
			add(filepath.Join(real, entry.Name()))
		}
	}
	return paths, truncated
}

// BrowserWorkspaceAvailable is a cheap registration check. Liveness is checked
// by list/open, not while serializing the tool schema.
func BrowserWorkspaceAvailable(cwd string) bool {
	paths, _ := browserCandidatePaths(cwd, "")
	return len(paths) != 0
}

func discoverBrowsers(ctx context.Context, cwd, selected string) browserDiscovery {
	paths, truncated := browserCandidatePaths(cwd, selected)
	out := browserDiscovery{Instances: []browserInstance{}, Truncated: truncated}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	selected = canonicalBrowserSocket(selected)
	for _, path := range paths {
		if ctx.Err() != nil {
			out.Truncated = true
			break
		}
		c, err := browserctl.Dial(path) // retain permission checks for every probe
		if err == nil {
			probe, stop := context.WithTimeout(ctx, 500*time.Millisecond)
			var info browserctl.Info
			info, err = c.Info(probe)
			stop()
			c.CloseIdleConnections()
			if err == nil && (info.APIVersion != browserctl.APIVersion || info.BrowserID == "") {
				err = fmt.Errorf("endpoint is not a kitty-browse %s instance", browserctl.APIVersion)
			}
			if err == nil {
				// Capability schemas are not needed in an instance/tab listing.
				info.Capabilities = nil
				out.Instances = append(out.Instances, browserInstance{Socket: path, Selected: path == selected, Info: info})
				continue
			}
		}
		out.Failures = append(out.Failures, browserDiscoveryFailure{Socket: path, Error: err.Error()})
	}
	return out
}

func (t *BrowserTool) socketPath() string {
	t.tabMu.Lock()
	path := t.boundSocket
	t.tabMu.Unlock()
	if path != "" {
		return path // an explicit attachment overrides the startup environment
	}
	return browserSocket()
}

func (t *BrowserTool) discovery(ctx context.Context) browserDiscovery {
	if t.discover != nil {
		return t.discover(ctx)
	}
	return discoverBrowsers(ctx, t.cwd, t.socketPath())
}

func (t *BrowserTool) bind(instance browserInstance) {
	t.tabMu.Lock()
	t.boundSocket, t.boundBrowser = instance.Socket, instance.Info.BrowserID
	t.lastTab = "" // refs and tab choices belong to the previous browser
	t.tabMu.Unlock()
}

func (t *BrowserTool) checkBinding(info browserctl.Info) error {
	t.tabMu.Lock()
	id := t.boundBrowser
	t.tabMu.Unlock()
	if id != "" && id != info.BrowserID {
		return fmt.Errorf("selected socket now serves browser %q, not attached browser %q; call list then attach explicitly", info.BrowserID, id)
	}
	return nil
}

func (t *BrowserTool) listInstances(ctx context.Context) agentcore.AgentToolResult {
	d := t.discovery(ctx)
	return t.listInstancesResult(d)
}

func (t *BrowserTool) listInstancesResult(d browserDiscovery) agentcore.AgentToolResult {
	out := map[string]any{"instances": d.Instances, "unavailable": d.Failures, "truncated": d.Truncated,
		"selected_socket": t.socketPath(), "hint": "Use attach with the browserId from instances[].info to select an instance. list never switches browsers."}
	// Keep the original tab-list shape for an already selected live endpoint.
	for _, instance := range d.Instances {
		if instance.Selected {
			out["browser"], out["backend"], out["tabs"] = instance.Info.BrowserID, instance.Info.Backend, instance.Info.Tabs
			if err := t.checkBinding(instance.Info); err != nil {
				out["selection_error"] = err.Error()
			}
		}
	}
	return agentcore.AgentToolResult{
		Content: agentcore.ContentList{agentcore.NewTextContent(tuiosJSON(out))},
		Details: map[string]any{"instances": len(d.Instances)},
	}
}

func (t *BrowserTool) attach(ctx context.Context, id string) (agentcore.AgentToolResult, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return browserError(fmt.Errorf("attach needs browser: the browserId from list")), nil
	}
	d := t.discovery(ctx) // re-probe, don't trust a cached inventory or supplied path
	for _, instance := range d.Instances {
		if instance.Info.BrowserID == id {
			t.bind(instance)
			return agentcore.AgentToolResult{
				Content: agentcore.ContentList{agentcore.NewTextContent(tuiosJSON(map[string]any{
					"attached": id, "socket": instance.Socket, "tabs": instance.Info.Tabs,
				}))},
				Details: map[string]any{"browser": id, "socket": instance.Socket},
			}, nil
		}
	}
	return browserError(fmt.Errorf("browser %q is not among the live discovered instances; call list again", id)), nil
}
