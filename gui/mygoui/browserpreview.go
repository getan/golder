package mygoui

import (
	"bytes"
	"context"
	"image"
	_ "image/png"
	"math"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
)

type browserWindowState struct {
	win     *mygo.Window
	surface *appsession.BrowserSurface
	bitmap  *ui.Bitmap
	address string
	pageURL string
	title   string
	err     string
	tabs    []appsession.BrowserTab

	frameW, frameH float32
	viewW, viewH   float32
	viewportW      int
	viewportH      int
}

// openBrowserPreview attaches the native interactive surface in the main
// window's right drawer. The Bitmap route remains toolkit-native and shares no
// terminal renderer.
func (a *App) openBrowserPreview(browser appsession.BrowserPreview) {
	a.closeBrowserPanel()
	ctx, cancel := context.WithCancel(context.Background())
	state := &browserWindowState{address: browser.URL, pageURL: browser.URL, title: browser.Title}
	if state.title == "" {
		state.title = browser.URL
	}
	state.win = a.win
	a.browserState, a.browserCancel, a.browserPanelOpen = state, cancel, true
	a.changesOpen = false
	go attachBrowserSurface(ctx, a.win, state, browser)
}

func (a *App) closeBrowserPanel() {
	if a.browserCancel != nil {
		a.browserCancel()
		a.browserCancel = nil
	}
	if a.browserState != nil && a.browserState.surface != nil {
		a.browserState.surface.Close()
	}
	a.browserState = nil
	a.browserPanelOpen = false
}

func (a *App) browserPanel(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Width(browserWidth).MinWidth(360).BorderWidth(0, 0, 0, 1).BorderColor(t.Border).Children(func() {
		ui.Row(c).Height(36).Padding(5, 10).Gap(8).AlignItems(ui.Center).
			BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			ui.Text(c, "🌐").FontSize(14)
			if a.browserState != nil {
				ui.Text(c, oneLine(a.browserState.title, 70)).FontSize(12).Grow(1).SingleLine().Ellipsis("…")
			} else {
				ui.Text(c, "Browser").FontSize(12).Grow(1)
			}
			if ui.Button(c, "×").Tooltip("Close browser panel").Clicked() {
				a.closeBrowserPanel()
			}
		})
		if a.browserState != nil {
			browserSurfaceView(c, a.browserState)
		}
	})
}

func attachBrowserSurface(ctx context.Context, win *mygo.Window, state *browserWindowState, preview appsession.BrowserPreview) {
	openCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	surface, err := appsession.OpenBrowserSurface(openCtx, preview)
	cancel()
	if err != nil {
		win.Update(func() { state.err = err.Error() })
		return
	}
	select {
	case <-ctx.Done():
		surface.Close()
		return
	default:
	}
	title, rawURL, _ := surface.State()
	win.Update(func() {
		state.surface = surface
		state.title, state.address, state.pageURL = title, rawURL, rawURL
	})

	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	frames := 0
	for {
		frameCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		raw, frameErr := surface.Frame(frameCtx)
		stop()
		var bitmap *ui.Bitmap
		var width, height float32
		if frameErr == nil {
			img, _, decodeErr := image.Decode(bytes.NewReader(raw))
			if decodeErr != nil {
				frameErr = decodeErr
			} else {
				bitmap = ui.NewBitmap(img)
				bounds := img.Bounds()
				width, height = float32(bounds.Dx()), float32(bounds.Dy())
			}
		}
		title, rawURL, inputErr := surface.State()
		var tabs []appsession.BrowserTab
		if frames%10 == 0 {
			tabsCtx, tabsCancel := context.WithTimeout(ctx, 3*time.Second)
			tabs, _ = surface.Tabs(tabsCtx)
			tabsCancel()
		}
		frames++
		if frameErr == nil {
			frameErr = inputErr
		}
		win.Update(func() {
			if bitmap != nil {
				state.bitmap, state.frameW, state.frameH = bitmap, width, height
			}
			state.title = title
			if tabs != nil {
				state.tabs = tabs
			}
			previousURL := state.pageURL
			state.pageURL = rawURL
			if state.address == "" || state.address == previousURL {
				state.address = rawURL
			}
			if frameErr != nil {
				state.err = frameErr.Error()
			} else {
				state.err = ""
			}
		})
		select {
		case <-ctx.Done():
			surface.Close()
			return
		case <-ticker.C:
		}
	}
}

func browserSurfaceView(c *ui.Context, state *browserWindowState) {
	theme := c.Theme()
	ui.Column(c).Fill().Background(theme.Background).Children(func() {
		ui.Row(c).Height(48).Padding(7, 10).Gap(7).AlignItems(ui.Center).
			BorderWidth(0, 0, 1, 0).BorderColor(theme.Border).Children(func() {
			browserChromeButton(c, "←", "Back", state.surface != nil, func() {
				runBrowserAction(state, func(ctx context.Context) error { return state.surface.Back(ctx) })
			})
			browserChromeButton(c, "→", "Forward", state.surface != nil, func() {
				runBrowserAction(state, func(ctx context.Context) error { return state.surface.Forward(ctx) })
			})
			browserChromeButton(c, "↻", "Reload", state.surface != nil, func() {
				runBrowserAction(state, func(ctx context.Context) error { return state.surface.Reload(ctx) })
			})
			address := ui.TextInput(c, &state.address).Grow(1).Placeholder("Enter a URL")
			address.OnSubmit(func() {
				if state.surface != nil && strings.TrimSpace(state.address) != "" {
					runBrowserAction(state, func(ctx context.Context) error {
						return state.surface.Navigate(ctx, state.address)
					})
				}
			})
			if ui.Button(c, "Open externally").Clicked() && state.address != "" {
				_ = mygo.Shell.OpenExternal(state.address)
			}
			ui.MenuButton(c, "Tabs", func(menu *ui.Menu) {
				for _, tab := range state.tabs {
					tab := tab
					label := strings.TrimSpace(tab.Title)
					if label == "" {
						label = tab.URL
					}
					if menu.Item(oneLine(label, 70)).Checked(tab.Active).Chosen() {
						runBrowserAction(state, func(ctx context.Context) error { return state.surface.ActivateTab(ctx, tab.ID) })
					}
				}
			})
			browserChromeButton(c, "+", "New tab", state.surface != nil, func() {
				runBrowserAction(state, func(ctx context.Context) error { return state.surface.NewTab(ctx, "about:blank") })
			})
			activeID := activeBrowserTab(state.tabs)
			browserChromeButton(c, "×", "Close tab", state.surface != nil && activeID != "", func() {
				runBrowserAction(state, func(ctx context.Context) error { return state.surface.CloseTab(ctx, activeID) })
			})
		})

		viewport := ui.Box(c).Grow(1).MinHeight(0).Background(theme.Surface).Focusable().InputMethod(true).
			TextCaret(ui.Rect{X: 8, Y: 8, W: 1, H: 18})
		viewport.HandleInput(func(event ui.InputEvent) bool {
			return handleBrowserInput(state, event)
		})
		viewport.Children(func() {
			switch {
			case state.bitmap != nil:
				ui.Image(c, state.bitmap).Fill().Fit(ui.Contain)
			case state.err != "":
				ui.Column(c).Fill().Center().Children(func() {
					ui.Text(c, state.err).FontSize(12).TextColor(theme.Danger).MaxWidth(600).TextAlign(ui.Center)
				})
			default:
				ui.Column(c).Fill().Center().Gap(8).Children(func() {
					ui.Spinner(c)
					ui.Text(c, "Connecting to Chromium…").FontSize(12).TextColor(theme.TextMuted)
				})
			}
		})
		bounds := viewport.Bounds()
		if bounds.W > 0 && bounds.H > 0 {
			state.viewW, state.viewH = bounds.W, bounds.H
			wantW, wantH := int(math.Round(float64(bounds.W))), int(math.Round(float64(bounds.H)))
			if state.surface != nil && wantW > 0 && wantH > 0 &&
				(absInt(wantW-state.viewportW) > 4 || absInt(wantH-state.viewportH) > 4) {
				state.viewportW, state.viewportH = wantW, wantH
				surface := state.surface
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					_ = surface.Resize(ctx, wantW, wantH)
				}()
			}
		}

		ui.Row(c).Height(30).Padding(0, 10).AlignItems(ui.Center).
			BorderWidth(1, 0, 0, 0).BorderColor(theme.Border).Children(func() {
			ui.Text(c, "Interactive Chromium · mouse, keyboard, scroll and IME enabled").FontSize(10).TextColor(theme.TextMuted)
			ui.Spacer(c)
			if state.err != "" {
				ui.Text(c, state.err).FontSize(10).TextColor(theme.Danger).SingleLine().Ellipsis("…").MaxWidth(420)
			}
		})
	})
}

func browserChromeButton(c *ui.Context, label, tooltip string, enabled bool, click func()) {
	if ui.Button(c, label).Disabled(!enabled).Tooltip(tooltip).Clicked() {
		click()
	}
}

func runBrowserAction(state *browserWindowState, action func(context.Context) error) {
	if state.surface == nil || state.win == nil {
		return
	}
	win := state.win
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := action(ctx)
		win.Update(func() {
			if err != nil {
				state.err = err.Error()
			}
		})
	}()
}

func handleBrowserInput(state *browserWindowState, event ui.InputEvent) bool {
	if state.surface == nil {
		return false
	}
	x, y, inside := browserViewportPoint(state, event.X, event.Y)
	switch event.Kind {
	case ui.InputPointerDown:
		if !inside {
			return false
		}
		return state.surface.Input(appsession.BrowserInput{Kind: appsession.BrowserPointerDown, X: x, Y: y, Button: event.Button, Clicks: event.Clicks})
	case ui.InputPointerMove:
		if !inside {
			return false
		}
		return state.surface.Input(appsession.BrowserInput{Kind: appsession.BrowserPointerMove, X: x, Y: y, Button: event.Button, Clicks: event.Clicks})
	case ui.InputPointerUp:
		if !inside {
			return false
		}
		return state.surface.Input(appsession.BrowserInput{Kind: appsession.BrowserPointerUp, X: x, Y: y, Button: event.Button, Clicks: event.Clicks})
	case ui.InputScroll:
		if !inside {
			return false
		}
		return state.surface.Input(appsession.BrowserInput{Kind: appsession.BrowserScroll, X: x, Y: y, DX: float64(event.DX), DY: float64(event.DY)})
	case ui.InputText:
		return state.surface.Input(appsession.BrowserInput{Kind: appsession.BrowserText, Text: event.Text})
	case ui.InputKeyDown:
		if key := browserKey(event.Key); key != "" {
			return state.surface.Input(appsession.BrowserInput{Kind: appsession.BrowserKey, Key: key})
		}
	}
	return false
}

func browserViewportPoint(state *browserWindowState, x, y float32) (float64, float64, bool) {
	if state.frameW <= 0 || state.frameH <= 0 || state.viewW <= 0 || state.viewH <= 0 {
		return 0, 0, false
	}
	scale := min(state.viewW/state.frameW, state.viewH/state.frameH)
	displayW, displayH := state.frameW*scale, state.frameH*scale
	offsetX, offsetY := (state.viewW-displayW)/2, (state.viewH-displayH)/2
	if x < offsetX || y < offsetY || x > offsetX+displayW || y > offsetY+displayH {
		return 0, 0, false
	}
	return float64((x - offsetX) / scale), float64((y - offsetY) / scale), true
}

func browserKey(key ui.Key) string {
	switch key {
	case ui.KeyBackspace:
		return "Backspace"
	case ui.KeyEnter:
		return "Enter"
	case ui.KeyLeft:
		return "ArrowLeft"
	case ui.KeyRight:
		return "ArrowRight"
	case ui.KeyUp:
		return "ArrowUp"
	case ui.KeyDown:
		return "ArrowDown"
	case ui.KeyTab:
		return "Tab"
	case ui.KeyEscape:
		return "Escape"
	case ui.KeyDelete:
		return "Delete"
	case ui.KeyHome:
		return "Home"
	case ui.KeyEnd:
		return "End"
	case ui.KeyPageUp:
		return "PageUp"
	case ui.KeyPageDown:
		return "PageDown"
	default:
		return ""
	}
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func activeBrowserTab(tabs []appsession.BrowserTab) string {
	for _, tab := range tabs {
		if tab.Active {
			return tab.ID
		}
	}
	return ""
}
