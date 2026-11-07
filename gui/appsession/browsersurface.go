package appsession

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/getan/golder/internal/browserctl"
	"github.com/getan/golder/internal/controlcap"
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
	Kind      BrowserInputKind
	X, Y      float64
	DX, DY    float64
	Button    int
	Clicks    int
	Text, Key string
}

// BrowserTab is safe native chrome metadata for one controlled tab.
type BrowserTab struct {
	ID     string
	Title  string
	URL    string
	Active bool
}

// BrowserSelection identifies one explicitly selected tab in one browser.
// Revision is zero only for the same-process compatibility event from a host
// which predates the control socket's explicit-selection revision.
type BrowserSelection struct {
	BrowserID string
	TabID     string
	TargetID  string
	Revision  uint64
}

// BrowserSurface is a terminal-independent view of one controlled Chromium
// tab. Frames come from the target-scoped screenshot action; input is serialized
// onto the same typed action socket. kitty-browse remains the CDP owner.
type BrowserSurface struct {
	client   *browserctl.Client
	operator BrowserOperator

	mu                sync.Mutex
	target            browserctl.Target
	title             string
	url               string
	err               error
	closed            bool
	dragging          bool
	dragTarget        browserctl.Target
	dragX, dragY      float64
	selectionRevision uint64

	input *browserInputQueue
	done  chan struct{}
	once  sync.Once
}

type surfaceInput struct {
	event  BrowserInput
	target browserctl.Target
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
	if preview.Browser != "" && preview.Browser != info.BrowserID {
		client.CloseIdleConnections()
		return nil, fmt.Errorf("browser surface identity changed since discovery")
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
		client: client, operator: browserOperator(preview.Socket), target: target, title: title, url: rawURL,
		input: newBrowserInputQueue(128), done: make(chan struct{}),
	}
	if info.ExplicitSelection != nil {
		surface.selectionRevision = info.ExplicitSelection.Revision
	}
	go surface.runInputs()
	return surface, nil
}

// Frame captures this surface's selected tab, independently of the host's
// globally active tab. A frame that raced a local tab switch is discarded.
func (s *BrowserSurface) Frame(ctx context.Context) ([]byte, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, fmt.Errorf("browser surface is closed")
	}
	target := s.target
	s.mu.Unlock()
	data, meta, err := s.client.ScreenshotTarget(ctx, target)
	if err != nil {
		return nil, err
	}
	if !s.updateSelectedMeta(target, meta) {
		return nil, fmt.Errorf("browser surface selection changed during capture")
	}
	return data, nil
}

// Resize changes this surface's selected Chromium tab. Dimensions are CSS pixels.
func (s *BrowserSurface) Resize(ctx context.Context, width, height int) error {
	if width < 1 || height < 1 {
		return nil
	}
	_, _, err := s.action(ctx, "set.viewport", map[string]any{"width": width, "height": height}, 5*time.Second)
	return err
}

// Navigate loads a URL through kitty-browse's validated action path.
func (s *BrowserSurface) Navigate(ctx context.Context, rawURL string) error {
	_, _, err := s.action(ctx, "navigate", map[string]any{"url": strings.TrimSpace(rawURL)}, 60*time.Second)
	return err
}

// Back, Forward and Reload expose native browser chrome actions.
func (s *BrowserSurface) Back(ctx context.Context) error    { return s.simple(ctx, "back") }
func (s *BrowserSurface) Forward(ctx context.Context) error { return s.simple(ctx, "forward") }
func (s *BrowserSurface) Reload(ctx context.Context) error  { return s.simple(ctx, "reload") }

// Tabs refreshes metadata without adopting another surface's selection. If the
// selected tab was closed, the browser's active tab becomes the fallback.
func (s *BrowserSurface) Tabs(ctx context.Context) ([]BrowserTab, error) {
	s.mu.Lock()
	previous := s.target
	s.mu.Unlock()
	info, err := s.client.Info(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || info.BrowserID != s.target.BrowserID {
		return nil, fmt.Errorf("browser surface identity changed or closed")
	}
	if s.target != previous {
		return nil, fmt.Errorf("browser surface selection changed during tab discovery")
	}
	selected := -1
	fallback := -1
	for i, tab := range info.Tabs {
		if tab.ID == s.target.TabID && tab.TargetID == s.target.TargetID {
			selected = i
		}
		if tab.Active {
			fallback = i
		}
	}
	if selected < 0 {
		selected = fallback
		if selected < 0 && len(info.Tabs) > 0 {
			selected = 0
		}
	}
	if selected >= 0 {
		s.selectTabLocked(info.BrowserID, info.Tabs[selected])
	}
	out := make([]BrowserTab, 0, len(info.Tabs))
	for i, tab := range info.Tabs {
		out = append(out, BrowserTab{ID: tab.ID, Title: tab.Title, URL: tab.URL, Active: i == selected})
	}
	return out, nil
}

// PendingSelection observes successful explicit control-API activations only.
// It never changes the local view or performs a browser action. A newer manual
// or browser selection invalidates an older event instead of replaying it.
func (s *BrowserSurface) PendingSelection(ctx context.Context) (*BrowserSelection, error) {
	s.mu.Lock()
	previous := s.target
	s.mu.Unlock()
	info, err := s.client.Info(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || info.BrowserID != previous.BrowserID {
		return nil, fmt.Errorf("browser surface identity changed or closed")
	}
	if s.target != previous {
		return nil, nil
	}
	selection := info.ExplicitSelection
	if selection == nil || selection.Revision == 0 || selection.Revision <= s.selectionRevision {
		return nil, nil
	}
	if selection.TabID == s.target.TabID && selection.TargetID == s.target.TargetID {
		s.selectionRevision = selection.Revision
		return nil, nil
	}
	if _, ok := selectionTab(info, selection.TabID, selection.TargetID); !ok {
		s.selectionRevision = selection.Revision
		return nil, nil
	}
	return &BrowserSelection{BrowserID: info.BrowserID, TabID: selection.TabID, TargetID: selection.TargetID, Revision: selection.Revision}, nil
}

// FollowSelection updates only this local surface, after rechecking the exact
// browser, tab, target and active state. It never activates or navigates a tab.
func (s *BrowserSurface) FollowSelection(ctx context.Context, selection BrowserSelection) error {
	if selection.BrowserID == "" || selection.TabID == "" || selection.TargetID == "" {
		return fmt.Errorf("browser selection requires exact browser, tab and target identities")
	}
	s.mu.Lock()
	previous := s.target
	s.mu.Unlock()
	info, err := s.client.Info(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || info.BrowserID != previous.BrowserID || selection.BrowserID != info.BrowserID {
		return fmt.Errorf("browser surface identity changed or closed")
	}
	if s.target != previous {
		return nil // a later local selection wins
	}
	if selection.Revision == 0 && info.ExplicitSelection != nil {
		return nil // revision-aware hosts are followed through their ordered signal
	}
	if selection.Revision != 0 {
		current := info.ExplicitSelection
		if selection.Revision <= s.selectionRevision || current == nil || current.Revision != selection.Revision || current.TabID != selection.TabID || current.TargetID != selection.TargetID {
			return nil
		}
	}
	tab, ok := selectionTab(info, selection.TabID, selection.TargetID)
	if !ok {
		if selection.Revision > s.selectionRevision {
			s.selectionRevision = selection.Revision
		}
		return nil
	}
	s.selectTabLocked(info.BrowserID, tab)
	if info.ExplicitSelection != nil && info.ExplicitSelection.Revision > s.selectionRevision {
		s.selectionRevision = info.ExplicitSelection.Revision
	}
	return nil
}

func selectionTab(info browserctl.Info, tabID, targetID string) (browserctl.Tab, bool) {
	if tabID == "" || targetID == "" || info.ActiveTabID != tabID {
		return browserctl.Tab{}, false
	}
	for _, tab := range info.Tabs {
		if tab.ID == tabID && tab.TargetID == targetID && tab.URL != "" && controlcap.BrowserURL(tab.URL) == nil {
			return tab, true
		}
	}
	return browserctl.Tab{}, false
}

// ActivateTab switches only this surface's view, not the browser host's active
// tab, which may be displayed by another surface.
func (s *BrowserSurface) ActivateTab(ctx context.Context, id string) error {
	info, err := s.client.Info(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || info.BrowserID != s.target.BrowserID {
		return fmt.Errorf("browser surface identity changed or closed")
	}
	for _, tab := range info.Tabs {
		if tab.ID == id && tab.TargetID != "" {
			if info.ExplicitSelection != nil && info.ExplicitSelection.Revision > s.selectionRevision {
				s.selectionRevision = info.ExplicitSelection.Revision
			}
			s.selectTabLocked(info.BrowserID, tab)
			return nil
		}
	}
	return fmt.Errorf("browser tab %q is no longer available", id)
}

// NewTab opens and activates a new tab.
func (s *BrowserSurface) NewTab(ctx context.Context, rawURL string) error {
	s.mu.Lock()
	previous := s.target
	s.mu.Unlock()
	params := map[string]any{}
	if rawURL = strings.TrimSpace(rawURL); rawURL != "" {
		params["url"] = rawURL
	}
	_, meta, err := s.client.Action(ctx, "tab.new", params, browserctl.Target{BrowserID: previous.BrowserID}, 30*time.Second)
	if err == nil {
		s.mu.Lock()
		if !s.closed && s.target == previous && meta.BrowserID == previous.BrowserID && meta.TabID != "" && meta.TargetID != "" {
			s.selectTabLocked(meta.BrowserID, browserctl.Tab{ID: meta.TabID, TargetID: meta.TargetID, Title: meta.Title, URL: meta.URL})
		}
		s.mu.Unlock()
	}
	return err
}

// CloseTab closes one tab; kitty-browse chooses the next active tab.
func (s *BrowserSurface) CloseTab(ctx context.Context, id string) error {
	s.mu.Lock()
	browserID := s.target.BrowserID
	s.mu.Unlock()
	_, _, err := s.client.Action(ctx, "tab.close", map[string]any{"tabId": id},
		browserctl.Target{BrowserID: browserID}, 10*time.Second)
	if err == nil {
		_, err = s.Tabs(ctx)
	}
	return err
}

func (s *BrowserSurface) simple(ctx context.Context, action string) error {
	_, _, err := s.action(ctx, action, map[string]any{}, 30*time.Second)
	return err
}

// Input queues a person-originated event without blocking MyGo's main thread.
// It returns false and reports an error if a boundary event cannot be queued.
func (s *BrowserSurface) Input(event BrowserInput) bool {
	s.mu.Lock()
	if s.closed || s.input == nil {
		s.mu.Unlock()
		return false
	}
	if s.operator == nil && (event.Kind == BrowserPointerDown || event.Kind == BrowserPointerMove || event.Kind == BrowserPointerUp) {
		s.err = fmt.Errorf("browser host does not support native pointer input")
		s.mu.Unlock()
		return false
	}
	target := s.target
	ok := s.input.push(surfaceInput{event: event, target: target})
	if !ok {
		s.err = fmt.Errorf("browser input queue is full; input was not sent")
	} else {
		s.err = nil
	}
	s.mu.Unlock()
	return ok
}

// NativePointerAvailable is a presentation fact, not permission to dispatch an
// Agent action through the in-process operator channel.
func (s *BrowserSurface) NativePointerAvailable() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && s.operator != nil
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
		s.releaseDragLocked()
		s.closed = true
		s.mu.Unlock()
		close(s.done)
		s.client.CloseIdleConnections()
	})
}

func (s *BrowserSurface) runInputs() {
	for {
		select {
		case <-s.done:
			return
		case <-s.input.wake:
			input, ok := s.input.pop()
			if !ok {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			err := s.dispatchInput(ctx, input)
			cancel()
			if err != nil {
				s.mu.Lock()
				s.err = err
				s.mu.Unlock()
			}
		}
	}
}

func (s *BrowserSurface) dispatchInput(ctx context.Context, input surfaceInput) error {
	s.mu.Lock()
	current := !s.closed && s.target == input.target
	s.mu.Unlock()
	if !current {
		return fmt.Errorf("browser input target changed; event was not sent")
	}
	event := input.event
	if s.operator != nil {
		return s.dispatchOperatorInput(ctx, input)
	}
	switch event.Kind {
	case BrowserScroll:
		_, _, err := s.actionTarget(ctx, input.target, "scroll", map[string]any{
			"x": event.X, "viewportY": event.Y, "deltaX": event.DX, "deltaY": event.DY,
		}, 5*time.Second)
		return err
	case BrowserText:
		if event.Text == "" {
			return nil
		}
		_, _, err := s.actionTarget(ctx, input.target, "keyboard.insert", map[string]any{"text": event.Text}, 5*time.Second)
		return err
	case BrowserKey:
		if event.Key == "" {
			return nil
		}
		_, _, err := s.actionTarget(ctx, input.target, "press", map[string]any{"key": event.Key}, 5*time.Second)
		return err
	case BrowserPointerDown, BrowserPointerMove, BrowserPointerUp:
		return fmt.Errorf("browser host does not support native pointer input")
	default:
		return nil
	}
}

func (s *BrowserSurface) dispatchOperatorInput(ctx context.Context, input surfaceInput) error {
	event := input.event
	var kind string
	switch event.Kind {
	case BrowserPointerDown:
		kind = "pointer-down"
	case BrowserPointerMove:
		s.mu.Lock()
		dragging := s.dragging && s.dragTarget == input.target
		s.mu.Unlock()
		if dragging {
			kind = "pointer-move"
		} else {
			kind = "pointer-hover"
		}
	case BrowserPointerUp:
		s.mu.Lock()
		dragging := s.dragging && s.dragTarget == input.target
		s.mu.Unlock()
		if !dragging {
			return nil
		}
		kind = "pointer-up"
	case BrowserScroll:
		kind = "scroll"
	case BrowserText:
		kind = "text"
	case BrowserKey:
		kind = "key"
	default:
		return nil
	}
	if (event.Kind == BrowserPointerDown || event.Kind == BrowserPointerUp) && event.Button != 0 {
		return fmt.Errorf("only the primary browser pointer button is supported")
	}
	if event.Kind == BrowserScroll && event.DX != 0 && event.DY == 0 {
		return nil
	}
	if err := s.sendOperatorInput(ctx, input.target, kind, event); err != nil {
		return err
	}
	s.mu.Lock()
	switch kind {
	case "pointer-down":
		if !s.closed && s.target == input.target {
			s.dragging, s.dragTarget, s.dragX, s.dragY = true, input.target, event.X, event.Y
		} else {
			s.scheduleDragRelease(input.target, event.X, event.Y)
		}
	case "pointer-move":
		if s.dragging && s.dragTarget == input.target {
			s.dragX, s.dragY = event.X, event.Y
		}
	case "pointer-up":
		if s.dragging && s.dragTarget == input.target {
			s.dragging = false
		}
	}
	s.mu.Unlock()
	return nil
}

func (s *BrowserSurface) sendOperatorInput(ctx context.Context, target browserctl.Target, kind string, event BrowserInput) error {
	return s.operator.SendOperatorInput(ctx, BrowserOperatorInput{
		BrowserID: target.BrowserID, TabID: target.TabID, TargetID: target.TargetID,
		Kind: kind, X: event.X, Y: event.Y, DeltaY: event.DY, Text: event.Text, Key: event.Key,
	})
}

func (s *BrowserSurface) scheduleDragRelease(target browserctl.Target, x, y float64) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.sendOperatorInput(ctx, target, "pointer-up", BrowserInput{X: x, Y: y})
	}()
}

// releaseDragLocked prevents a tab switch from leaving a pressed pointer in a
// different surface's page. The release stays pinned to the original target.
func (s *BrowserSurface) releaseDragLocked() {
	if s.dragging && s.operator != nil {
		s.scheduleDragRelease(s.dragTarget, s.dragX, s.dragY)
		s.dragging = false
	}
}

func (s *BrowserSurface) action(ctx context.Context, action string, params any, timeout time.Duration) (json.RawMessage, browserctl.Meta, error) {
	s.mu.Lock()
	target := s.target
	s.mu.Unlock()
	return s.actionTarget(ctx, target, action, params, timeout)
}

func (s *BrowserSurface) actionTarget(ctx context.Context, target browserctl.Target, action string, params any, timeout time.Duration) (json.RawMessage, browserctl.Meta, error) {
	result, meta, err := s.client.Action(ctx, action, params, target, timeout)
	if err == nil {
		s.updateSelectedMeta(target, meta)
	}
	return result, meta, err
}

func (s *BrowserSurface) selectTabLocked(browserID string, tab browserctl.Tab) {
	target := browserctl.Target{BrowserID: browserID, TabID: tab.ID, TargetID: tab.TargetID}
	if s.target != target {
		s.releaseDragLocked()
	}
	s.target = target
	s.title, s.url = tab.Title, tab.URL
}

// updateSelectedMeta cannot let a slow response from a previous tab change the
// current target, even when that response arrives after a user switches tabs.
func (s *BrowserSurface) updateSelectedMeta(target browserctl.Target, meta browserctl.Meta) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.target != target || meta.BrowserID != target.BrowserID ||
		meta.TabID != target.TabID || meta.TargetID != target.TargetID {
		return false
	}
	if meta.Title != "" {
		s.title = meta.Title
	}
	if meta.URL != "" {
		s.url = meta.URL
	}
	return true
}
