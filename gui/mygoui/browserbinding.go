package mygoui

import (
	"context"
	"time"

	"github.com/getan/golder/gui/appsession"
	"github.com/getan/golder/internal/browserbinding"
)

// This is a subscription, not a second browser selection. Replacing it also
// invalidates callbacks from an earlier visit to the same controller (A-B-A).
type browserSessionView struct {
	ctrl    *appsession.Controller
	binding *browserbinding.Binding
	id      string
	ctx     context.Context
	cancel  context.CancelFunc
	refresh chan struct{}
	seen    uint64
}

func (a *App) resetBrowserSession() {
	if a.browserSession != nil {
		a.browserSession.cancel()
		a.browserSession = nil
	}
	a.closeBrowserPanel()
	a.browserOpen = false
	a.browsers = nil
}

func (a *App) startBrowserSession() {
	if a.ctrl == nil {
		return
	}
	binding := a.ctrl.BrowserBinding()
	if binding == nil || binding.Snapshot().SessionID != a.status.SessionID {
		return
	}
	if current := a.browserSession; current != nil && current.ctrl == a.ctrl && current.binding == binding {
		return
	}
	a.resetBrowserSession()
	ctx, cancel := context.WithCancel(context.Background())
	view := &browserSessionView{ctrl: a.ctrl, binding: binding, id: a.status.SessionID, ctx: ctx, cancel: cancel, refresh: make(chan struct{}, 1)}
	a.browserSession = view
	win := a.win
	if win == nil {
		return // headless view tests explicitly deliver snapshots
	}
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			state, browsers := view.ctrl.BrowserSnapshot(ctx, binding)
			if ctx.Err() != nil {
				return
			}
			win.Update(func() { a.applyBrowserSnapshot(view, state, browsers) })
			select {
			case <-ctx.Done():
				return
			case <-state.Changed:
			case <-view.refresh:
			case <-ticker.C:
			}
		}
	}()
}

func (a *App) refreshBrowsers() {
	a.startBrowserSession()
	if view := a.browserSession; view != nil {
		select {
		case view.refresh <- struct{}{}:
		default:
		}
	}
}

func (a *App) currentBrowserView(view *browserSessionView) bool {
	return view != nil && a.browserSession == view && a.ctrl == view.ctrl &&
		a.status.SessionID == view.id && view.ctx.Err() == nil && view.ctrl.BrowserBinding() == view.binding
}

func (a *App) applyBrowserSnapshot(view *browserSessionView, state browserbinding.Snapshot, browsers []appsession.BrowserPreview) {
	if !a.currentBrowserView(view) || state.SessionID != view.id || view.binding.Snapshot().Revision != state.Revision {
		return
	}
	a.browsers = append([]appsession.BrowserPreview(nil), browsers...)
	endpoint := state.Endpoint
	if a.browserState != nil && (a.browserState.socket != endpoint.Socket || a.browserState.browserID != endpoint.BrowserID) {
		a.closeBrowserPanel()
	}
	if endpoint.Socket == "" || state.Reveal <= view.seen {
		return
	}
	for _, preview := range browsers {
		if preview.Endpoint() == endpoint {
			view.seen = state.Reveal
			if !a.browserPanelOpen {
				a.openBrowserPreview(preview)
			}
			return
		}
	}
}

// A cached row requests a validated selection; it cannot directly attach a
// surface. Only the resulting binding snapshot can open the drawer.
func (a *App) requestBrowserPreview(preview appsession.BrowserPreview) {
	a.startBrowserSession()
	view, win := a.browserSession, a.win
	if view == nil || win == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(view.ctx, 5*time.Second)
		defer cancel()
		err := view.ctrl.SelectBrowser(ctx, view.binding, preview)
		win.Update(func() {
			if a.currentBrowserView(view) && err != nil {
				a.banner, a.bannerErr = err.Error(), true
			}
		})
	}()
}

func (a *App) hideBrowserPanel() {
	if view := a.browserSession; view != nil {
		view.seen = view.binding.Snapshot().Reveal
	}
	a.closeBrowserPanel()
}

func browserSocketKey(path string) string { return browserbinding.SocketKey(path) }
