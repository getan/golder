package appsession

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/getan/golder/internal/browserctl"
)

// BrowserInputKind is one person-originated event for a native browser surface.
type BrowserInputKind uint8

const (
	BrowserPointerDown BrowserInputKind = iota + 1
	BrowserPointerUp
	BrowserPointerMove
	BrowserScroll
	BrowserText
	BrowserKey
)

// BrowserInput carries viewport CSS coordinates. Text is committed IME input;
// Key is a non-printing browser key such as Enter or ArrowLeft.
type BrowserInput struct {
	Kind       BrowserInputKind
	X, Y       float64
	DX, DY     float64
	Button     int
	Clicks     int
	Text, Key  string
}

// BrowserTab is safe native chrome metadata for one controlled tab.
type BrowserTab struct {
	ID     string
	Title  string
	URL    string
	Active bool
}

// BrowserSurface is a terminal-independent view of one controlled Chromium
// tab. Frames come from the existing screenshot endpoint; input is serialized
// onto the same typed action socket. kitty-browse remains the CDP owner.
type BrowserSurface struct {
	client *browserctl.Client

	mu     sync.Mutex
	target browserctl.Target
	title  string
	url    string
	err    error
	closed bool

	input chan BrowserInput
	done  chan struct{}
	once  sync.Once
}

// OpenBrowserSurface attaches to a browser already admitted by the owner-only
// socket and starts its serialized manual-input worker.
func OpenBrowserSurface(ctx context.Context, preview BrowserPreview) (*BrowserSurface, error) {
	client, err := browserctlDial(preview.Socket)
	if err != nil {
		return nil, err
	}
	info, err := client.Info(ctx)
	if err != nil {
		client.CloseIdleConnections()
		return nil, err
	}
	target := browserctl.Target{BrowserID: info.BrowserID, TabID: info.ActiveTabID}
	title, rawURL := preview.Title, preview.URL
	for _, tab := range info.Tabs {
		if tab.Active {
			target.TabID, target.TargetID = tab.ID, tab.TargetID
			title, rawURL = tab.Title, tab.URL
			break
		}
	}
	surface := &BrowserSurface{
		client: client, target: target, title: title, url: rawURL,
		input: make(chan BrowserInput, 128), done: make(chan struct{}),
	}
	go surface.runInputs()
	return surface, nil
}

// Frame captures the current viewport as PNG. The endpoint already bounds the
// response and uses kitty-browse's idle/still recovery path.
func (s *BrowserSurface) Frame(ctx context.Context) ([]byte, error) {
	if s.isClosed() {
		return nil, fmt.Errorf("browser surface is closed")
	}
	return s.client.Screenshot(ctx, false)
}

// Resize changes the active Chromium viewport. Dimensions are CSS pixels.
func (s *BrowserSurface) Resize(ctx context.Context, width, height int) error {
	if width < 1 || height < 1 {
		return nil
	}
	_, _, err := s.action(ctx, "set.viewport", map[string]any{"width": width, "height": height}, 5*time.Second)
	return err
}

// Navigate loads a URL through kitty-browse's validated action path.
func (s *BrowserSurface) Navigate(ctx context.Context, rawURL string) error {
	_, meta, err := s.action(ctx, "navigate", map[string]any{"url": strings.TrimSpace(rawURL)}, 60*time.Second)
	if err == nil {
		s.updateMeta(meta)
	}
	return err
}

// Back, Forward and Reload expose native browser chrome actions.
func (s *BrowserSurface) Back(ctx context.Context) error    { return s.simple(ctx, "back") }
func (s *BrowserSurface) Forward(ctx context.Context) error { return s.simple(ctx, "forward") }
func (s *BrowserSurface) Reload(ctx context.Context) error  { return s.simple(ctx, "reload") }

// Tabs refreshes the browser's tab strip metadata.
func (s *BrowserSurface) Tabs(ctx context.Context) ([]BrowserTab, error) {
	info, err := s.client.Info(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]BrowserTab, 0, len(info.Tabs))
	for _, tab := range info.Tabs {
		out = append(out, BrowserTab{ID: tab.ID, Title: tab.Title, URL: tab.URL, Active: tab.Active})
		if tab.Active {
			s.updateMeta(browserctl.Meta{
				BrowserID: info.BrowserID, TabID: tab.ID, TargetID: tab.TargetID,
				Title: tab.Title, URL: tab.URL,
			})
		}
	}
	return out, nil
}

// ActivateTab switches the native surface to an existing tab.
func (s *BrowserSurface) ActivateTab(ctx context.Context, id string) error {
	s.mu.Lock()
	browserID := s.target.BrowserID
	s.mu.Unlock()
	_, meta, err := s.client.Action(ctx, "tab.activate", map[string]any{"tabId": id},
		browserctl.Target{BrowserID: browserID}, 10*time.Second)
	if err == nil {
		s.updateMeta(meta)
	}
	return err
}

// NewTab opens and activates a new tab.
func (s *BrowserSurface) NewTab(ctx context.Context, rawURL string) error {
	s.mu.Lock()
	browserID := s.target.BrowserID
	s.mu.Unlock()
	params := map[string]any{}
	if rawURL = strings.TrimSpace(rawURL); rawURL != "" {
		params["url"] = rawURL
	}
	_, meta, err := s.client.Action(ctx, "tab.new", params, browserctl.Target{BrowserID: browserID}, 30*time.Second)
	if err == nil {
		s.updateMeta(meta)
	}
	return err
}

// CloseTab closes one tab; kitty-browse chooses the next active tab.
func (s *BrowserSurface) CloseTab(ctx context.Context, id string) error {
	s.mu.Lock()
	browserID := s.target.BrowserID
	s.mu.Unlock()
	_, meta, err := s.client.Action(ctx, "tab.close", map[string]any{"tabId": id},
		browserctl.Target{BrowserID: browserID}, 10*time.Second)
	if err == nil {
		s.updateMeta(meta)
	}
	return err
}

func (s *BrowserSurface) simple(ctx context.Context, action string) error {
	_, meta, err := s.action(ctx, action, map[string]any{}, 30*time.Second)
	if err == nil {
		s.updateMeta(meta)
	}
	return err
}

// Input queues a person-originated event without blocking MyGo's main thread.
// Under sustained control-socket pressure a new event is consumed but dropped;
// the UI must stay responsive even when Chromium is hung.
func (s *BrowserSurface) Input(event BrowserInput) bool {
	if s.isClosed() {
		return false
	}
	select {
	case s.input <- event:
		return true
	default:
		return true
	}
}

// State returns display metadata and the last input error, without exposing the
// control socket or any page content.
func (s *BrowserSurface) State() (title, rawURL string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.title, s.url, s.err
}

func (s *BrowserSurface) Close() {
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		close(s.done)
		s.client.CloseIdleConnections()
	})
}

func (s *BrowserSurface) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *BrowserSurface) runInputs() {
	for {
		select {
		case <-s.done:
			return
		case event := <-s.input:
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			err := s.dispatchInput(ctx, event)
			cancel()
			s.mu.Lock()
			s.err = err
			s.mu.Unlock()
		}
	}
}

func (s *BrowserSurface) dispatchInput(ctx context.Context, event BrowserInput) error {
	switch event.Kind {
	case BrowserScroll:
		_, _, err := s.action(ctx, "scroll", map[string]any{
			"x": event.X, "viewportY": event.Y, "deltaX": event.DX, "deltaY": event.DY,
		}, 5*time.Second)
		return err
	case BrowserText:
		if event.Text == "" {
			return nil
		}
		_, _, err := s.action(ctx, "keyboard.insert", map[string]any{"text": event.Text}, 5*time.Second)
		return err
	case BrowserKey:
		if event.Key == "" {
			return nil
		}
		_, _, err := s.action(ctx, "press", map[string]any{"key": event.Key}, 5*time.Second)
		return err
	case BrowserPointerDown, BrowserPointerMove, BrowserPointerUp:
		script := pointerScript(event)
		if script == "" {
			return nil
		}
		_, _, err := s.action(ctx, "eval", map[string]any{"script": script}, 5*time.Second)
		return err
	default:
		return nil
	}
}

// pointerScript is a compatibility adapter for kitty-browse v1 control sockets,
// whose public action schema exposes locator clicks but not the raw Session
// MouseDown/Move/Up methods used by its terminal pane. It accepts only numeric
// coordinates and a closed event-kind set, so no page-controlled text enters JS.
// A future raw-input capability can replace this adapter without changing the
// GUI BrowserSurface contract.
func pointerScript(event BrowserInput) string {
	typ := ""
	switch event.Kind {
	case BrowserPointerDown:
		typ = "mousedown"
	case BrowserPointerMove:
		typ = "mousemove"
	case BrowserPointerUp:
		typ = "mouseup"
	default:
		return ""
	}
	x := strconv.FormatFloat(event.X, 'f', 3, 64)
	y := strconv.FormatFloat(event.Y, 'f', 3, 64)
	button := event.Button
	if button < 0 || button > 2 {
		button = 0
	}
	clicks := event.Clicks
	if clicks < 1 {
		clicks = 1
	}
	return fmt.Sprintf(`(()=>{const x=%s,y=%s,e=document.elementFromPoint(x,y);if(!e)return false;`+
		`e.dispatchEvent(new MouseEvent(%q,{bubbles:true,cancelable:true,clientX:x,clientY:y,button:%d,detail:%d}));`+
		`%sreturn true})()`, x, y, typ, button, clicks, pointerActivation(event.Kind, button, clicks))
}

func pointerActivation(kind BrowserInputKind, button, clicks int) string {
	if kind == BrowserPointerDown {
		return `if(typeof e.focus==="function")e.focus();`
	}
	if kind != BrowserPointerUp || button != 0 {
		return ""
	}
	if clicks > 1 {
		return `e.dispatchEvent(new MouseEvent("dblclick",{bubbles:true,cancelable:true,clientX:x,clientY:y,detail:2}));`
	}
	return `if(typeof e.click==="function")e.click();`
}

func (s *BrowserSurface) action(ctx context.Context, action string, params any, timeout time.Duration) (json.RawMessage, browserctl.Meta, error) {
	s.mu.Lock()
	target := s.target
	s.mu.Unlock()
	result, meta, err := s.client.Action(ctx, action, params, target, timeout)
	if err == nil {
		s.updateMeta(meta)
	}
	return result, meta, err
}

func (s *BrowserSurface) updateMeta(meta browserctl.Meta) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if meta.BrowserID != "" {
		s.target.BrowserID = meta.BrowserID
	}
	if meta.TabID != "" {
		s.target.TabID = meta.TabID
	}
	if meta.TargetID != "" {
		s.target.TargetID = meta.TargetID
	}
	if meta.Title != "" {
		s.title = meta.Title
	}
	if meta.URL != "" {
		s.url = meta.URL
	}
}
