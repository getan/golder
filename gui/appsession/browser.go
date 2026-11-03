package appsession

import (
	"context"

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
