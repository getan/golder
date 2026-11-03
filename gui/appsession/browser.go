package appsession

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/getan/golder/internal/browserctl"
)

// browserctlDial is the permission-checked browser client constructor. It is a
// variable so tests can substitute a transport without a real socket.
var browserctlDial = browserctl.Dial

// BrowserScreenshot returns the active tab's viewport as PNG. The caller draws
// it; nothing here needs a terminal graphics protocol.
func (c *Controller) BrowserScreenshot(ctx context.Context, socket string, fullPage bool) ([]byte, error) {
	client, err := browserctlDial(socket)
	if err != nil {
		return nil, err
	}
	defer client.CloseIdleConnections()
	return client.Screenshot(ctx, fullPage)
}

// BrowserPreview is one live browser page the GUI can show.
type BrowserPreview struct {
	Socket   string
	Browser  string
	TabID    string
	TargetID string
	URL      string
	Title    string
}

// Browsers discovers Golder browser control sockets under the project. It is
// independent of tuios workspace discovery: a native GUI browser talks to the
// browser owner directly.
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
				preview.TabID, preview.TargetID = tab.ID, tab.TargetID
				preview.URL, preview.Title = tab.URL, tab.Title
			}
		}
		out = append(out, preview)
	}
	return out
}

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
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".sock") || entry.Type()&os.ModeSocket == 0 {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			if !seen[path] {
				seen[path] = true
				out = append(out, path)
			}
		}
	}
	sort.Strings(out)
	return out
}
