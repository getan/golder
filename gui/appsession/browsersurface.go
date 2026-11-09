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

// BrowserSurface is a terminal-independent view of one controlled Chromium
// tab. Frames come from the target-scoped screenshot action; input is serialized
// onto the same typed action socket. kitty-browse remains the CDP owner.
type BrowserSurface struct {
	client   *browserctl.Client
	operator BrowserOperator

	mu              sync.Mutex
	target          browserctl.Target
	title           string
	url             string
	err             error
	closed          bool
	dragging        bool
	dragTarget      browserctl.Target
	dragX, dragY    float64
	refreshSequence uint64

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
	tab, err := browserctl.Foreground(info)
	if err != nil {
		client.CloseIdleConnections()
		return nil, err
	}
	target := browserctl.Target{BrowserID: info.BrowserID, TabID: tab.ID, TargetID: tab.TargetID}
	surface := &BrowserSurface{
		client: client, operator: browserOperator(preview.Socket), target: target, title: tab.Title, url: tab.URL,
		input: newBrowserInputQueue(128), done: make(chan struct{}),
	}
	go surface.runInputs()
	return surface, nil
}

// BrowserFrame keeps the pixels, chrome and input target from one capture
// together. Input must be pinned to the displayed frame, not a newer refresh.
type BrowserFrame struct {
	PNG    []byte
	Target browserctl.Target
	Title  string
	URL    string
	Tabs   []BrowserTab
}

func (s *BrowserSurface) Frame(ctx context.Context) ([]byte, error) {
	frame, err := s.Capture(ctx)
	return frame.PNG, err
}

// Capture projects the host foreground and rechecks it at both ends of capture.
func (s *BrowserSurface) Capture(ctx context.Context) (BrowserFrame, error) {
	if _, err := s.Tabs(ctx); err != nil {
		return BrowserFrame{}, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return BrowserFrame{}, fmt.Errorf("browser surface is closed")
	}
	target := s.target
	s.mu.Unlock()
	data, meta, err := s.client.ScreenshotTarget(ctx, target)
	if err != nil {
		return BrowserFrame{}, err
	}
	tabs, err := s.Tabs(ctx)
	if err != nil {
		return BrowserFrame{}, err
	}
	if !s.updateSelectedMeta(target, meta) {
		return BrowserFrame{}, fmt.Errorf("browser surface selection changed during capture")
	}
	return BrowserFrame{PNG: data, Target: target, Title: meta.Title, URL: meta.URL, Tabs: tabs}, nil
}

// Resize changes this surface's selected Chromium tab. Dimensions are CSS pixels.
func (s *BrowserSurface) Resize(ctx context.Context, width, height int) error {
	s.mu.Lock()
	target := s.target
	s.mu.Unlock()
	return s.ResizeTarget(ctx, target, width, height)
}

// ResizeTarget keeps deferred resize work attached to the frame that requested
// it. A host selection change cannot resize a newly foregrounded tab by mistake.
func (s *BrowserSurface) ResizeTarget(ctx context.Context, target browserctl.Target, width, height int) error {
	if width < 1 || height < 1 {
		return nil
	}
	s.mu.Lock()
	current := !s.closed && s.target == target && target.TabID != ""
	s.mu.Unlock()
	if !current {
		return fmt.Errorf("browser resize target changed")
	}
	_, _, err := s.actionTarget(ctx, target, "set.viewport", map[string]any{"width": width, "height": height}, 5*time.Second)
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

// Tabs is a projection of the host foreground, not a second selection store.
// An older concurrent Info response must not overwrite a newer refresh.
func (s *BrowserSurface) Tabs(ctx context.Context) ([]BrowserTab, error) {
	s.mu.Lock()
	s.refreshSequence++
	sequence := s.refreshSequence
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
	if sequence != s.refreshSequence {
		return nil, fmt.Errorf("browser surface refresh superseded")
	}
	foreground, err := browserctl.Foreground(info)
	if err != nil {
		s.releaseDragLocked()
		s.target = browserctl.Target{BrowserID: info.BrowserID}
		s.title, s.url = "", ""
		return nil, err
	}
	s.selectTabLocked(info.BrowserID, foreground)
	out := make([]BrowserTab, 0, len(info.Tabs))
	for _, tab := range info.Tabs {
		out = append(out, BrowserTab{ID: tab.ID, Title: tab.Title, URL: tab.URL, Active: tab.ID == foreground.ID})
	}
	return out, nil
}

// ActivateTab uses the host selection command, exactly as Agent and TUI do.
// Operator authentication remains separate from Agent automation permission.
func (s *BrowserSurface) ActivateTab(ctx context.Context, id string) error {
	info, err := s.client.Info(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed || info.BrowserID != s.target.BrowserID {
		s.mu.Unlock()
		return fmt.Errorf("browser surface identity changed or closed")
	}
	s.refreshSequence++ // invalidate earlier reads before dispatch
	s.mu.Unlock()
	for _, tab := range info.Tabs {
		if tab.ID == id && tab.TargetID != "" {
			if err := controlcap.BrowserURL(tab.URL); err != nil {
				return err
			}
			target := browserctl.Target{BrowserID: info.BrowserID, TabID: tab.ID, TargetID: tab.TargetID}
			_, meta, err := s.surfaceAction(ctx, "tab.activate", map[string]any{"tabId": id}, target, 10*time.Second)
			if err != nil {
				return err
			}
			if meta.BrowserID != target.BrowserID || meta.TabID != target.TabID || meta.TargetID != target.TargetID {
				return fmt.Errorf("activated browser target changed")
			}
			_, err = s.Tabs(ctx)
			return err
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
	_, _, err := s.surfaceAction(ctx, "tab.new", params, browserctl.Target{BrowserID: previous.BrowserID}, 30*time.Second)
	if err == nil {
		_, err = s.Tabs(ctx)
	}
	return err
}

// CloseTab closes one tab; kitty-browse chooses the next active tab.
func (s *BrowserSurface) CloseTab(ctx context.Context, id string) error {
	s.mu.Lock()
	browserID := s.target.BrowserID
	s.mu.Unlock()
	_, _, err := s.surfaceAction(ctx, "tab.close", map[string]any{"tabId": id},
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
	target := s.target
	s.mu.Unlock()
	return s.InputTarget(target, event)
}

// InputTarget rejects input for pixels superseded by a host/manual selection.
func (s *BrowserSurface) InputTarget(target browserctl.Target, event BrowserInput) bool {
	s.mu.Lock()
	if s.closed || s.input == nil || target.TabID == "" || target != s.target {
		s.mu.Unlock()
		return false
	}
	if s.operator == nil && (event.Kind == BrowserPointerDown || event.Kind == BrowserPointerMove || event.Kind == BrowserPointerUp) {
		s.err = fmt.Errorf("browser host does not support native pointer input")
		s.mu.Unlock()
		return false
	}
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
	result, meta, err := s.surfaceAction(ctx, action, params, target, timeout)
	if err == nil {
		s.updateSelectedMeta(target, meta)
	}
	return result, meta, err
}

func (s *BrowserSurface) surfaceAction(ctx context.Context, action string, params any, target browserctl.Target, timeout time.Duration) (json.RawMessage, browserctl.Meta, error) {
	if operator, ok := s.operator.(BrowserChromeOperator); ok {
		switch action {
		case "navigate", "back", "forward", "reload", "tab.new", "tab.activate", "tab.close", "set.viewport", "interaction.resume", "interaction.pause":
			return operator.ChromeAction(ctx, action, params, target, timeout)
		}
	}
	return s.client.Action(ctx, action, params, target, timeout)
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
