package main

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/getan/kitty-browse/browserhost"

	"github.com/getan/golder/internal/agenttool"
)

// nativeBrowserHosts owns headless Chromium instances opened by the desktop
// Agent. Their frames are rendered by MyGo's Bitmap Browser Surface; no terminal
// pane or second golder executable is involved.
type nativeBrowserHosts struct {
	mu    sync.Mutex
	hosts map[*browserhost.Host]bool
}

func newNativeBrowserHosts() *nativeBrowserHosts {
	return &nativeBrowserHosts{hosts: map[*browserhost.Host]bool{}}
}

func (h *nativeBrowserHosts) Start(ctx context.Context, req agenttool.NativeBrowserRequest) error {
	if req.ProxyServer != "" {
		return fmt.Errorf("the native GUI browser host does not yet support the configured browser proxy")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	opts := browserhost.DefaultOptions()
	opts.URL = req.URL
	opts.Socket = req.Socket
	opts.ProfileDir = req.ProfileDir
	opts.LoginProfile = req.LoginProfile
	opts.Chrome = os.Getenv("KITTY_BROWSE_CHROME")
	opts.BrowserOutput = os.Stderr
	opts.RequireSandbox = true
	host, err := browserhost.Start(ctx, opts)
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.hosts[host] = true
	h.mu.Unlock()
	go func() {
		_ = host.Wait()
		h.mu.Lock()
		delete(h.hosts, host)
		h.mu.Unlock()
	}()
	return nil
}

func (h *nativeBrowserHosts) Close() {
	h.mu.Lock()
	hosts := make([]*browserhost.Host, 0, len(h.hosts))
	for host := range h.hosts {
		hosts = append(hosts, host)
	}
	h.mu.Unlock()
	for _, host := range hosts {
		_ = host.Close()
	}
}
