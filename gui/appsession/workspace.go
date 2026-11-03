package appsession

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/getan/golder/internal/cli/workspacecmd"
	"github.com/getan/golder/internal/golderhome"
	"github.com/getan/golder/internal/tuiosctl"
)

// WorkspaceInfo is one named terminal workspace, as the GUI lists it.
type WorkspaceInfo struct {
	Session string
	Cwd     string
	Socket  string
	Running bool
	Panes   int
	Error   string
}

// PaneInfo is one pane of a workspace.
type PaneInfo struct {
	ID       string
	Title    string
	Name     string
	Program  string
	Cwd      string
	Focused  bool
	Agent    string
	Revision uint64
}

// Workspaces discovers the named sessions under the golder state root and
// probes each endpoint. Discovery never starts a daemon.
func (c *Controller) Workspaces(ctx context.Context) ([]WorkspaceInfo, error) {
	home := golderhome.Dir()
	if home == "" {
		return nil, fmt.Errorf("cannot resolve the golder state directory")
	}
	opts, err := workspacecmd.Discover(home)
	if err != nil {
		return nil, err
	}
	out := make([]WorkspaceInfo, 0, len(opts))
	for _, o := range opts {
		info := WorkspaceInfo{Session: o.Session, Cwd: o.Cwd, Socket: o.Socket, Error: o.DiscoveryError}
		if o.DiscoveryError == "" && o.Socket != "" {
			probe, cancel := context.WithTimeout(ctx, 2*time.Second)
			client, derr := tuiosctl.Dial(probe, tuiosctl.Options{
				Socket: o.Socket, Session: o.Session, Version: "golder-gui",
			})
			if derr == nil {
				list, lerr := client.ListWindows(probe)
				_ = client.Close()
				if lerr == nil {
					info.Running = true
					info.Panes = len(list.Windows)
				} else {
					info.Error = lerr.Error()
				}
			} else {
				info.Error = derr.Error()
			}
			cancel()
		}
		out = append(out, info)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Running != out[j].Running {
			return out[i].Running
		}
		return out[i].Session < out[j].Session
	})
	return out, nil
}

// WorkspacePanes lists one workspace's panes.
func (c *Controller) WorkspacePanes(ctx context.Context, session string) ([]PaneInfo, error) {
	socket, err := c.workspaceSocket(session)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	client, err := tuiosctl.Dial(ctx, tuiosctl.Options{Socket: socket, Session: session, Version: "golder-gui"})
	if err != nil {
		return nil, err
	}
	defer client.Close()
	list, err := client.ListWindows(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]PaneInfo, 0, len(list.Windows))
	for _, w := range list.Windows {
		out = append(out, PaneInfo{
			ID:       w.WindowID,
			Title:    w.Title,
			Name:     w.DisplayName,
			Program:  w.ForegroundCmd,
			Cwd:      w.Cwd,
			Focused:  w.Focused,
			Agent:    w.AgentState,
			Revision: w.Revision,
		})
	}
	return out, nil
}

// CapturePane returns a pane's screen as text, bounded to lines rows.
func (c *Controller) CapturePane(ctx context.Context, session, pane string, lines int) (string, error) {
	socket, err := c.workspaceSocket(session)
	if err != nil {
		return "", err
	}
	if lines <= 0 {
		lines = 80
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	client, err := tuiosctl.Dial(ctx, tuiosctl.Options{Socket: socket, Session: session, Version: "golder-gui"})
	if err != nil {
		return "", err
	}
	defer client.Close()
	captured, err := client.CapturePane(ctx, pane, "recent", lines, false)
	if err != nil {
		return "", err
	}
	return captured.Content, nil
}

// workspaceSocket resolves one named session's socket without starting it.
func (c *Controller) workspaceSocket(session string) (string, error) {
	home := golderhome.Dir()
	if home == "" {
		return "", fmt.Errorf("cannot resolve the golder state directory")
	}
	opts, err := workspacecmd.Discover(home)
	if err != nil {
		return "", err
	}
	for _, o := range opts {
		if o.Session == session && o.Socket != "" {
			return o.Socket, nil
		}
	}
	return "", fmt.Errorf("workspace %q is not running", session)
}

// BrowserPreview is one live browser page the GUI can show read-only.
type BrowserPreview struct {
	Socket  string
	Browser string
	URL     string
	Title   string
}

// Browsers discovers Golder browser panes under the project's .golder
// directory. Discovery is shallow and bounded: profile directories are never
// walked.
func (c *Controller) Browsers(ctx context.Context) []BrowserPreview {
	c.mu.Lock()
	cwd := c.cwd
	c.mu.Unlock()
	sockets := browserSockets(cwd)
	out := make([]BrowserPreview, 0, len(sockets))
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for _, socket := range sockets {
		client, err := browserctlDial(socket)
		if err != nil {
			continue
		}
		probe, stop := context.WithTimeout(ctx, 800*time.Millisecond)
		info, ierr := client.Info(probe)
		stop()
		client.CloseIdleConnections()
		if ierr != nil {
			continue
		}
		preview := BrowserPreview{Socket: socket, Browser: info.BrowserID}
		for _, tab := range info.Tabs {
			if tab.Active {
				preview.URL, preview.Title = tab.URL, tab.Title
			}
		}
		out = append(out, preview)
	}
	return out
}

// browserSockets lists candidate control sockets in the project's .golder
// directories. A socket is a candidate only if it is a socket file; the typed
// client's permission check still decides whether it may be used.
func browserSockets(cwd string) []string {
	if strings.TrimSpace(cwd) == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, dir := range []string{filepath.Join(cwd, ".golder", "browser"), filepath.Join(cwd, ".golder")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		if len(entries) > 64 {
			entries = entries[:64]
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".sock") || e.Type()&os.ModeSocket == 0 {
				continue
			}
			p := filepath.Join(dir, e.Name())
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	sort.Strings(out)
	return out
}
