package appsession

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/getan/golder/internal/session"
)

// SessionEvent identifies the controller that produced an event. A view must
// not apply an event to a different session after switching conversations.
type SessionEvent struct {
	Controller *Controller
	Event      Event
}

type hubEntry struct {
	controller      *Controller
	unread          bool
	failed          bool
	computerRequest string
}

// SessionHub owns independent controllers so turns, tools, approvals and
// persistence remain attached to their originating session while the view
// switches between conversations.
type SessionHub struct {
	mu          sync.Mutex
	switchMu    sync.Mutex
	current     *Controller
	entries     map[string]*hubEntry
	events      chan SessionEvent
	done        chan struct{}
	closed      bool
	authorizing bool
}

func NewSessionHub(initial *Controller) *SessionHub {
	h := &SessionHub{current: initial, entries: make(map[string]*hubEntry), events: make(chan SessionEvent, 1024), done: make(chan struct{})}
	if initial != nil {
		id := initial.SessionID()
		h.entries[id] = &hubEntry{controller: initial}
		go h.watch(id, initial)
	}
	return h
}

func (h *SessionHub) Events() <-chan SessionEvent { return h.events }
func (h *SessionHub) Done() <-chan struct{}       { return h.done }

func (h *SessionHub) Current() *Controller {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.current
}

func (h *SessionHub) watch(id string, controller *Controller) {
	for {
		select {
		case <-h.done:
			return
		case <-controller.done:
			return
		case event := <-controller.Events():
			h.mu.Lock()
			entry := h.entries[id]
			if entry == nil || entry.controller != controller {
				h.mu.Unlock()
				return
			}
			active := h.current == controller
			if event.Kind == EventComputerRequest {
				entry.computerRequest = event.ID
			}
			if event.Kind == EventRunEnd && event.Error != "" {
				entry.computerRequest = ""
			}
			if event.Kind == EventRunEnd && !active {
				entry.unread = true
				entry.failed = event.Error != ""
			}
			h.mu.Unlock()
			if !active && event.Kind != EventStatus && event.Kind != EventApproval && event.Kind != EventApprovalDone && event.Kind != EventRunEnd && event.Kind != EventComputerRequest {
				continue
			}
			select {
			case h.events <- SessionEvent{Controller: controller, Event: event}:
			case <-h.done:
				return
			case <-controller.done:
				return
			}
		}
	}
}

// Resume selects an already-owned live controller or opens an independent peer
// for a saved session. It never cancels the previously selected controller.
func (h *SessionHub) Resume(ctx context.Context, id string) (*Controller, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("resume: empty session id")
	}
	if err := session.ValidateID(id); err != nil {
		return nil, err
	}
	h.switchMu.Lock()
	defer h.switchMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h.mu.Lock()
	if h.closed || h.current == nil {
		h.mu.Unlock()
		return nil, ErrClosed
	}
	if h.authorizing {
		h.mu.Unlock()
		return nil, errors.New("finish private authorization before switching sessions")
	}
	if entry := h.entries[id]; entry != nil {
		h.current = entry.controller
		entry.unread, entry.failed = false, false
		h.mu.Unlock()
		return entry.controller, nil
	}
	current := h.current
	h.mu.Unlock()
	peer, err := current.newPeer(ctx, id, "")
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = peer.Close()
		return nil, err
	}
	return h.install(peer)
}

// NewSession creates a fresh independent controller for the chosen project.
func (h *SessionHub) NewSession(ctx context.Context, project string) (*Controller, error) {
	h.switchMu.Lock()
	defer h.switchMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h.mu.Lock()
	if h.closed || h.current == nil {
		h.mu.Unlock()
		return nil, ErrClosed
	}
	if h.authorizing {
		h.mu.Unlock()
		return nil, errors.New("finish private authorization before opening another session")
	}
	current := h.current
	h.mu.Unlock()
	peer, err := current.newPeer(ctx, "", project)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = peer.Close()
		return nil, err
	}
	return h.install(peer)
}

func (h *SessionHub) install(peer *Controller) (*Controller, error) {
	id := peer.SessionID()
	h.mu.Lock()
	if h.closed || h.authorizing {
		authorizing := h.authorizing
		h.mu.Unlock()
		_ = peer.Close()
		if authorizing {
			return nil, errors.New("finish private authorization before opening another session")
		}
		return nil, ErrClosed
	}
	if h.entries[id] != nil {
		h.mu.Unlock()
		_ = peer.Close()
		return nil, fmt.Errorf("session %s is already open", id)
	}
	h.entries[id] = &hubEntry{controller: peer}
	h.current = peer
	h.mu.Unlock()
	go h.watch(id, peer)
	return peer, nil
}

// TakeComputerRequest retrieves a background request only after its turn has
// ended. It remains tied to its originating controller and is never acted on
// while another conversation is visible.
func (h *SessionHub) TakeComputerRequest(controller *Controller) string {
	if controller == nil || controller.IsRunning() {
		return ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	entry := h.entries[controller.SessionID()]
	if entry == nil || entry.controller != controller || h.current != controller {
		return ""
	}
	request := entry.computerRequest
	entry.computerRequest = ""
	return request
}

func (h *SessionHub) ClearComputerRequest(controller *Controller) {
	if controller == nil {
		return
	}
	id := controller.SessionID()
	h.mu.Lock()
	defer h.mu.Unlock()
	if entry := h.entries[id]; entry != nil && entry.controller == controller {
		entry.computerRequest = ""
	}
}

// ParkComputerRequest keeps an in-progress local picker tied to its original
// conversation if the view switches before the person finishes selecting.
func (h *SessionHub) ParkComputerRequest(controller *Controller, request string) {
	if controller == nil || request == "" {
		return
	}
	id := controller.SessionID()
	h.mu.Lock()
	defer h.mu.Unlock()
	if entry := h.entries[id]; entry != nil && entry.controller == controller {
		entry.computerRequest = request
	}
}

// ListSessions merges stored history with all live controllers, including
// unsaved new sessions and background approvals/completions.
func (h *SessionHub) ListSessions(ctx context.Context) ([]SessionSummary, error) {
	h.mu.Lock()
	current := h.current
	entries := make(map[string]hubEntry, len(h.entries))
	for id, entry := range h.entries {
		entries[id] = *entry
	}
	h.mu.Unlock()
	if current == nil {
		return nil, ErrClosed
	}
	rows, err := current.ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	index := make(map[string]int, len(rows))
	for i := range rows {
		rows[i].Current = rows[i].ID == current.SessionID()
		index[rows[i].ID] = i
	}
	for id, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		peer := entry.controller
		peer.mu.Lock()
		live := SessionSummary{ID: id, Title: peer.title, Model: peer.live.Model, Provider: peer.live.ProviderName,
			Cwd: peer.cwd, UpdatedAt: peer.header.UpdatedAt, Current: peer == current,
			Running: peer.running, NeedsApproval: len(peer.pending) > 0 || entry.computerRequest != "" && !peer.running, Unread: entry.unread, Failed: entry.failed}
		peer.mu.Unlock()
		if live.Title == "" {
			live.Title = "New session"
		}
		if i, ok := index[id]; ok {
			live.Archived = rows[i].Archived
			if rows[i].UpdatedAt.After(live.UpdatedAt) {
				live.UpdatedAt = rows[i].UpdatedAt
			}
			rows[i] = live
		} else {
			rows = append(rows, live)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Current != rows[j].Current {
			return rows[i].Current
		}
		return rows[i].UpdatedAt.After(rows[j].UpdatedAt)
	})
	return rows, nil
}

// RenameSession routes owned sessions to their own controller so their writer
// lock and in-memory title stay authoritative.
func (h *SessionHub) RenameSession(ctx context.Context, id, title string) error {
	h.switchMu.Lock()
	defer h.switchMu.Unlock()
	h.mu.Lock()
	current, entry := h.current, h.entries[id]
	h.mu.Unlock()
	if current == nil {
		return ErrClosed
	}
	if entry != nil {
		if entry.controller.IsRunning() {
			return ErrBusy
		}
		return entry.controller.RenameSession(ctx, id, title)
	}
	return current.RenameSession(ctx, id, title)
}

func (h *SessionHub) ArchiveSession(ctx context.Context, id string, archived bool) error {
	return h.removeSession(ctx, id, true, func(c *Controller) error { return c.ArchiveSession(ctx, id, archived) })
}

func (h *SessionHub) DeleteSession(ctx context.Context, id string) error {
	return h.removeSession(ctx, id, false, func(c *Controller) error { return c.DeleteSession(ctx, id) })
}

func (h *SessionHub) removeSession(ctx context.Context, id string, preserveEmpty bool, action func(*Controller) error) error {
	h.switchMu.Lock()
	defer h.switchMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	h.mu.Lock()
	current, entry := h.current, h.entries[id]
	h.mu.Unlock()
	if current == nil {
		return ErrClosed
	}
	if entry != nil && entry.controller == current {
		return errors.New("cannot remove the current session")
	}
	if entry != nil && entry.controller.IsRunning() {
		return errors.New("cannot remove a session while its turn is running")
	}
	missing := false
	if entry != nil {
		peer := entry.controller
		peer.mu.Lock()
		_, err := os.Stat(filepath.Join(peer.store.Dir(), session.FileName(id)))
		missing = errors.Is(err, os.ErrNotExist)
		if missing && preserveEmpty {
			if err := peer.store.Save(peer.header, peer.agentCtx.Messages); err != nil {
				peer.mu.Unlock()
				return err
			}
			missing = false
		}
		peer.mu.Unlock()
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		h.mu.Lock()
		delete(h.entries, id)
		h.mu.Unlock()
	}
	if entry != nil {
		if err := entry.controller.Close(); err != nil {
			return err
		}
	}
	if missing {
		return nil
	}
	return action(current)
}

func (h *SessionHub) ExportSession(id, format, path string) error {
	h.switchMu.Lock()
	defer h.switchMu.Unlock()
	h.mu.Lock()
	current, entry := h.current, h.entries[id]
	h.mu.Unlock()
	if current == nil {
		return ErrClosed
	}
	if entry != nil {
		if entry.controller.IsRunning() {
			return ErrBusy
		}
		return entry.controller.ExportSession(id, format, path)
	}
	return current.ExportSession(id, format, path)
}

// Close revokes all controllers, including background runs and pending grants.
func (h *SessionHub) Close() error {
	h.switchMu.Lock()
	defer h.switchMu.Unlock()
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	close(h.done)
	entries := h.entries
	h.entries = nil
	h.current = nil
	h.mu.Unlock()
	var errs []error
	for _, entry := range entries {
		if err := entry.controller.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
