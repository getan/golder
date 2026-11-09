package main

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/getan/kitty-browse/browserhost"

	"github.com/getan/golder/gui/appsession"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/browserpool"
)

// nativeBrowserHosts owns headless Chromium instances opened by the desktop
// Agent. Their frames are rendered by MyGo's Bitmap Browser Surface; no terminal
// pane or second golder executable is involved.
type nativeBrowserHosts struct {
	mu       sync.Mutex
	hosts    map[nativeBrowserHost]bool
	remote   map[*browserhost.RemoteView]func()
	starting map[context.Context]context.CancelFunc
	closed   bool
	start    func(context.Context, browserhost.Options) (nativeBrowserHost, error)
}

type nativeBrowserHost interface {
	Wait() error
	Close() error
}

type nativeBrowserInput interface {
	SendOperatorInput(context.Context, browserhost.OperatorInput) error
}

// A dependency regression must fail at build time instead of silently falling
// back to page-script input. The adapter keeps GUI and library types separate.
var _ nativeBrowserInput = (*browserhost.Host)(nil)

type nativeOperatorAdapter struct{ host nativeBrowserInput }

func (a nativeOperatorAdapter) SendOperatorInput(ctx context.Context, in appsession.BrowserOperatorInput) error {
	return a.host.SendOperatorInput(ctx, browserhost.OperatorInput{
		BrowserID: in.BrowserID, TabID: in.TabID, TargetID: in.TargetID,
		Kind: in.Kind, X: in.X, Y: in.Y, DeltaY: in.DeltaY, Text: in.Text, Key: in.Key,
	})
}

func newNativeBrowserHosts() *nativeBrowserHosts {
	return &nativeBrowserHosts{
		hosts:    map[nativeBrowserHost]bool{},
		starting: map[context.Context]context.CancelFunc{},
		start: func(ctx context.Context, opts browserhost.Options) (nativeBrowserHost, error) {
			return browserhost.Start(ctx, opts)
		},
	}
}

func (h *nativeBrowserHosts) Start(ctx context.Context, req agenttool.NativeBrowserRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if req.ConversationID != "" {
		opts, err := browserpool.Options(req.ProfileDir, req.LoginProfile, req.ProxyServer)
		if err != nil {
			return err
		}
		view, err := browserpool.Open(ctx, opts, req.ConversationID, req.Instance, req.URL)
		if err != nil {
			return err
		}
		if view.Socket != req.Socket {
			return fmt.Errorf("shared browser returned another session's socket")
		}
		h.mu.Lock()
		if h.closed {
			h.mu.Unlock()
			_ = view.Close()
			return fmt.Errorf("browser host is shutting down")
		}
		if h.remote == nil {
			h.remote = make(map[*browserhost.RemoteView]func())
		}
		h.remote[view] = appsession.RegisterSharedBrowser(view)
		h.mu.Unlock()
		return nil
	}
	h.mu.Lock()
	closed := h.closed
	h.mu.Unlock()
	if closed {
		return fmt.Errorf("native browser host is shutting down")
	}
	opts := browserhost.DefaultOptions()
	opts.URL = req.URL
	opts.Socket = req.Socket
	opts.ProfileDir = req.ProfileDir
	opts.LoginProfile = req.LoginProfile
	// The configured route is explicit and reviewed by the launcher; the host
	// rejects credentials or malformed values rather than falling back to direct.
	opts.ProxyServer = req.ProxyServer
	opts.Chrome = os.Getenv("KITTY_BROWSE_CHROME")
	opts.BrowserOutput = os.Stderr
	opts.RequireSandbox = true
	// The tool call controls startup, but a successful browser belongs to the
	// GUI host and must survive cancellation of that individual tool call.
	life, cancel := context.WithCancel(context.WithoutCancel(ctx))
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		cancel()
		return fmt.Errorf("native browser host is shutting down")
	}
	h.starting[life] = cancel
	h.mu.Unlock()
	stop := context.AfterFunc(ctx, cancel)
	host, err := h.start(life, opts)
	h.mu.Lock()
	delete(h.starting, life)
	h.mu.Unlock()
	if err != nil {
		stop()
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	if !stop() || ctx.Err() != nil {
		cancel()
		_ = host.Close()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return context.Canceled
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		cancel()
		_ = host.Close()
		return fmt.Errorf("native browser host is shutting down")
	}
	h.hosts[host] = true
	unregister := func() {}
	if operator, ok := host.(nativeBrowserInput); ok {
		unregister = appsession.RegisterBrowserOperator(req.Socket, nativeOperatorAdapter{host: operator})
	}
	h.mu.Unlock()
	go func() {
		defer cancel()
		_ = host.Wait()
		unregister()
		h.mu.Lock()
		delete(h.hosts, host)
		h.mu.Unlock()
	}()
	return nil
}

func (h *nativeBrowserHosts) Close() {
	h.mu.Lock()
	h.closed = true
	pending := make([]context.CancelFunc, 0, len(h.starting))
	for _, cancel := range h.starting {
		pending = append(pending, cancel)
	}
	hosts := make([]nativeBrowserHost, 0, len(h.hosts))
	for host := range h.hosts {
		hosts = append(hosts, host)
	}
	remotes := h.remote
	h.remote = nil
	h.mu.Unlock()
	for view, unregister := range remotes {
		_ = view.Close()
		unregister()
	}
	for _, cancel := range pending {
		cancel()
	}
	for _, host := range hosts {
		_ = host.Close()
	}
}
