package agenttool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/getan/kitty-browse/browserhost"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/browserpool"
)

// ConfigureBrowserSession binds broker tools before a conversation starts or
// changes. Neither model arguments nor workspace metadata supply the identity.
func ConfigureBrowserSession(tools []agentcore.AgentTool, id string) {
	seen := map[*ControlGuard]bool{}
	for _, raw := range tools {
		var g *ControlGuard
		switch t := raw.(type) {
		case *BrowserTool:
			g = t.Control
		case *WorkspaceTool:
			g = t.Control
		case *TuiosTool:
			g = t.Control
		}
		if g == nil || seen[g] {
			continue
		}
		seen[g] = true
		g.mu.Lock()
		if g.browserSession != id {
			g.browserSession = id
			g.browserSelected = ""
			g.browsers = map[string]controlledBrowser{}
			g.browserProfiles = nil
		}
		g.mu.Unlock()
	}
	for _, raw := range tools {
		if t, ok := raw.(*BrowserTool); ok {
			t.tabMu.Lock()
			if t.boundSession != id {
				t.boundSession = id
				t.boundSocket, t.boundBrowser, t.lastTab, t.lastTargetID = "", "", "", ""
			}
			t.tabMu.Unlock()
		}
	}
}

func (g *ControlGuard) conversation() string {
	if g == nil {
		return ""
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.browserSession
}

func (g *ControlGuard) selectBrowser(socket string) {
	if g == nil || g.conversation() == "" {
		setBrowserSocket(socket)
		return
	}
	g.mu.Lock()
	g.browserSelected = socket
	g.mu.Unlock()
}

func (t *BrowserTool) scopedDiscovery(ctx context.Context) browserDiscovery {
	paths := BrowserSessionSocketPaths(t.cwd, t.Control.conversation())
	return discoverBrowserPaths(ctx, paths, false, t.socketPath())
}

func sessionBrowserRecords(cwd, conversation string) []browserMetadata {
	if conversation == "" {
		return nil
	}
	dir := browserStateDir(cwd)
	root, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return nil
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil || !insidePath(root, real) {
		return nil
	}
	f, err := os.Open(real)
	if err != nil {
		return nil
	}
	entries, _ := f.ReadDir(256)
	_ = f.Close()
	var records []browserMetadata
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".browser.json") {
			continue
		}
		meta, err := readBrowserMetadata(filepath.Join(real, entry.Name()))
		if err != nil || meta.ConversationID != conversation || checkBrowserRecord(cwd, meta) != nil {
			continue
		}
		records = append(records, meta)
		if len(records) == maxBrowserCandidates {
			break
		}
	}
	return records
}

// BrowserSessionSocketPaths is also used by GUI discovery: a session sees only
// its own views, never another GUI/TUI conversation's project-default browser.
func BrowserSessionSocketPaths(cwd, conversation string) []string {
	var paths []string
	for _, meta := range sessionBrowserRecords(cwd, conversation) {
		paths = append(paths, canonicalBrowserSocket(meta.Socket))
	}
	return paths
}

// ConnectSessionBrowser restores an operator surface only for this trusted
// controller's conversation and a validated derived endpoint.
func ConnectSessionBrowser(ctx context.Context, cwd, conversation, socket string) (*browserhost.RemoteView, error) {
	for _, meta := range sessionBrowserRecords(cwd, conversation) {
		if canonicalBrowserSocket(meta.Socket) != canonicalBrowserSocket(socket) {
			continue
		}
		opts, err := browserpool.Options(meta.Profile, browserLoginProfile(cwd, meta.PersistentProfile), "")
		if err != nil {
			return nil, err
		}
		return browserhost.ReconnectShared(ctx, opts, browserhost.ViewID(conversation, meta.Instance))
	}
	return nil, fmt.Errorf("browser is not part of this conversation")
}

// CloseSessionBrowsers releases only the closing controller's views. It uses
// authenticated reconnect rather than trusting a repository's socket record.
func CloseSessionBrowsers(ctx context.Context, cwd, conversation string) {
	for _, socket := range BrowserSessionSocketPaths(cwd, conversation) {
		view, err := ConnectSessionBrowser(ctx, cwd, conversation, socket)
		if err == nil {
			_ = view.Close()
		}
		if ctx.Err() != nil {
			return
		}
	}
}

func sessionSpec(g *ControlGuard, cwd string, spec paneSpec) (paneSpec, error) {
	id := g.conversation()
	if id == "" {
		return spec, nil
	}
	if !spec.Embedded && !NativeBrowserLauncherAvailable() {
		return paneSpec{}, fmt.Errorf("session-isolated browser views require Golder's embedded browser host")
	}
	opts, err := browserpool.Options(spec.Profile, browserLoginProfile(cwd, spec.PersistentProfile), spec.ProxyServer)
	if err != nil {
		return paneSpec{}, err
	}
	instance := spec.Name + "/" + spec.PersistentProfile
	viewID := browserhost.ViewID(id, instance)
	spec.ConversationID, spec.Instance = id, instance
	spec.Socket = browserhost.ViewSocket(opts.RuntimeDir, viewID)
	spec.RecordName = "view-" + viewID
	for i := range spec.Argv {
		if spec.Argv[i] == "-control-socket" && i+1 < len(spec.Argv) {
			spec.Argv[i+1] = spec.Socket
		}
	}
	if spec.Embedded {
		last := len(spec.Argv) - 1
		args := append([]string{}, spec.Argv[:last]...)
		args = append(args, "-golder-session", id, "-golder-instance", instance, "-golder-state-root", filepath.Dir(filepath.Dir(opts.StateDir)))
		spec.Argv = append(args, spec.Argv[last])
	}
	return spec, nil
}

func checkSessionBrowserRecord(root string, meta browserMetadata) error {
	profile, err := persistentBrowserProfile(root, meta.PersistentProfile, false)
	if err != nil {
		return err
	}
	opts, err := browserpool.Options(profile, browserLoginProfile(root, meta.PersistentProfile), "")
	if err != nil {
		return err
	}
	id := browserhost.ViewID(meta.ConversationID, meta.Instance)
	if meta.Name != "view-"+id || canonicalBrowserSocket(meta.Profile) != canonicalBrowserSocket(profile) || canonicalBrowserSocket(meta.Socket) != canonicalBrowserSocket(browserhost.ViewSocket(opts.RuntimeDir, id)) {
		return fmt.Errorf("browser record does not match its conversation view")
	}
	return nil
}
