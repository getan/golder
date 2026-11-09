package appsession

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/browserbinding"
	"github.com/getan/golder/internal/browserctl"
)

// browserctlDial is the permission-checked browser client constructor. It is a
// variable so tests can substitute a transport without a real socket.
var browserctlDial = browserctl.Dial

// BrowserPreview is one live browser page the GUI can show.
type BrowserPreview struct {
	Socket   string
	Browser  string
	TabID    string
	TargetID string
	URL      string
	Title    string
}

func (p BrowserPreview) Endpoint() browserbinding.Endpoint {
	return browserbinding.Endpoint{Socket: browserbinding.SocketKey(p.Socket), BrowserID: p.Browser}
}

// BrowserBinding is the same object used by this controller's broker tools.
// A session change creates a new object, invalidating old GUI observations.
func (c *Controller) BrowserBinding() *browserbinding.Binding {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.browser
}

func (c *Controller) currentBrowserBinding(binding *browserbinding.Binding) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.closed && binding != nil && c.browser == binding
}

// BrowserSnapshot reconciles lifecycle only, not foreground selection. Tab
// selection belongs to the host, and an ambiguous inventory never switches it.
func (c *Controller) BrowserSnapshot(ctx context.Context, binding *browserbinding.Binding) (browserbinding.Snapshot, []BrowserPreview) {
	if !c.currentBrowserBinding(binding) {
		return browserbinding.Snapshot{}, nil
	}
	browsers := c.Browsers(ctx)
	if !c.currentBrowserBinding(binding) || ctx.Err() != nil {
		return browserbinding.Snapshot{}, nil
	}
	if len(browsers) == 1 {
		binding.Restore(browsers[0].Endpoint())
	}
	state := binding.Snapshot()
	if endpoint := state.Endpoint; endpoint.Socket != "" {
		if _, err := os.Stat(endpoint.Socket); os.IsNotExist(err) {
			binding.Clear(state)
			state = binding.Snapshot()
		}
	}
	return state, browsers
}

// SelectBrowser revalidates the session and live identity before changing the
// shared binding. A stale GUI row is never authority to attach another session.
func (c *Controller) SelectBrowser(ctx context.Context, binding *browserbinding.Binding, preview BrowserPreview) error {
	if !c.currentBrowserBinding(binding) {
		return fmt.Errorf("browser conversation changed")
	}
	for _, live := range c.Browsers(ctx) {
		if live.Endpoint() != preview.Endpoint() {
			continue
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.closed || c.browser != binding || ctx.Err() != nil {
			return fmt.Errorf("browser conversation changed or selection cancelled")
		}
		binding.Select(live.Endpoint())
		return nil
	}
	return fmt.Errorf("browser is no longer available in this conversation")
}

// Browsers discovers Golder browser control sockets under the project. It is
// independent of tuios workspace discovery: a native GUI browser talks to the
// browser owner directly.
func (c *Controller) Browsers(ctx context.Context) []BrowserPreview {
	c.mu.Lock()
	cwd, sessionID, readOnly := c.cwd, c.header.ID, c.readOnly
	c.mu.Unlock()
	sockets := agenttool.BrowserSessionSocketPaths(cwd, sessionID)
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
		if !readOnly && browserOperator(socket) == nil {
			if view, err := agenttool.ConnectSessionBrowser(ctx, cwd, sessionID, socket); err == nil && view.BrowserID == info.BrowserID {
				RegisterSharedBrowser(view)
			}
		}
		preview := BrowserPreview{Socket: socket, Browser: info.BrowserID}
		if tab, err := browserctl.Foreground(info); err == nil {
			preview.TabID, preview.TargetID = tab.ID, tab.TargetID
			preview.URL, preview.Title = tab.URL, tab.Title
		}
		out = append(out, preview)
	}
	return out
}
