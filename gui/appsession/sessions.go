package appsession

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/cli"
	"github.com/getan/golder/internal/cli/run"
	"github.com/getan/golder/internal/session"
)

// persist appends the messages produced since the last save as a branch from
// the active leaf, or rewrites the file linearly after a compaction.
func (c *Controller) persist() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.store == nil || c.agentCtx == nil {
		return errors.New("session is not open")
	}
	if c.readOnly {
		return nil
	}
	msgs := c.agentCtx.Messages
	if c.compacted || c.persisted > len(msgs) {
		c.header.UpdatedAt = time.Now().UTC()
		c.header.Model = c.live.Model
		c.header.Provider = c.live.ProviderName
		if err := c.store.Save(c.header, msgs); err != nil {
			return err
		}
		c.persisted = len(msgs)
		c.curLeaf = ""
		if _, entries, err := c.store.LoadEntries(c.header.ID); err == nil && len(entries) > 0 {
			c.curLeaf = entries[len(entries)-1].ID
		}
		c.compacted = false
		return nil
	}
	tail := msgs[c.persisted:]
	if len(tail) == 0 {
		return nil
	}
	c.header.UpdatedAt = time.Now().UTC()
	c.header.Model = c.live.Model
	c.header.Provider = c.live.ProviderName
	leaf, err := c.store.AppendBranch(c.header, c.curLeaf, tail)
	if err != nil {
		return err
	}
	c.curLeaf = leaf
	c.persisted = len(msgs)
	return nil
}

// ListSessions returns the stored sessions, newest first, marking the current.
func (c *Controller) ListSessions(ctx context.Context) ([]SessionSummary, error) {
	c.mu.Lock()
	store := c.store
	current := c.header.ID
	c.mu.Unlock()
	if store == nil {
		return nil, errors.New("session is not open")
	}
	headers, err := store.List()
	if err != nil {
		return nil, err
	}
	out := make([]SessionSummary, 0, len(headers))
	found := false
	for _, h := range headers {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if h.ID == current {
			found = true
		}
		out = append(out, SessionSummary{
			ID:        h.ID,
			Title:     sessionTitle(store, h),
			Model:     h.Model,
			Provider:  h.Provider,
			Cwd:       h.Cwd,
			UpdatedAt: h.UpdatedAt,
			Current:   h.ID == current,
			Archived:  h.ArchivedAt != nil,
		})
	}
	if !found {
		// A session exists in memory from Open onward, but its file appears only
		// with the first persisted message. The sidebar still shows it, so a new
		// window never looks like it has no conversation.
		c.mu.Lock()
		summary := SessionSummary{
			ID:        c.header.ID,
			Title:     c.title,
			Model:     c.live.Model,
			Provider:  c.live.ProviderName,
			Cwd:       c.cwd,
			UpdatedAt: c.header.UpdatedAt,
			Current:   true,
		}
		c.mu.Unlock()
		if summary.Title == "" {
			summary.Title = "New session"
		}
		out = append(out, summary)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Current != out[j].Current {
			return out[i].Current
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

// sessionTitle reads the first user line of a stored session without loading
// the whole file, falling back to its directory.
func sessionTitle(store *session.Store, h session.SessionHeader) string {
	if name := strings.TrimSpace(h.DisplayName); name != "" {
		return name
	}
	if preview, err := store.Preview(h.ID); err == nil {
		if line := strings.TrimSpace(strings.SplitN(preview, "\n", 2)[0]); line != "" {
			if r := []rune(line); len(r) > 60 {
				return string(r[:60]) + "…"
			}
			return line
		}
	}
	if h.Cwd != "" {
		return cli.SessionDirDisplay(h)
	}
	return h.ID
}

// Resume switches to a stored session after saving the current one.
func (c *Controller) Resume(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("resume: empty session id")
	}
	if c.hasUnsettledInput() {
		return ErrBusy
	}
	if err := c.persist(); err != nil {
		return fmt.Errorf("save current session: %w", err)
	}
	c.mu.Lock()
	store := c.store
	current := c.header.ID
	c.mu.Unlock()
	if id == current {
		return fmt.Errorf("already on session %s", id)
	}
	h, entries, err := store.LoadEntries(id)
	if err != nil {
		return err
	}
	c.StopComputer()
	if strings.TrimSpace(h.Cwd) != "" {
		target, targetErr := canonicalProjectPath(h.Cwd)
		if targetErr != nil {
			return targetErr
		}
		c.mu.Lock()
		currentProject := c.cwd
		c.mu.Unlock()
		if !sameProjectPath(target, currentProject) {
			next, prepareErr := c.prepareProjectRuntime(ctx, target)
			if prepareErr != nil {
				return prepareErr
			}
			lock, readOnly, holder, lockErr := acquireSessionAccess(store.Dir(), id)
			if lockErr != nil {
				closeProjectRuntime(next)
				return lockErr
			}
			c.installProjectSession(ctx, next, h, entries, lock, readOnly, holder, true)
			_ = c.SelectProject(target)
			return nil
		}
	}
	if err := c.reaccess(id); err != nil {
		return err
	}
	c.adopt(h, entries)
	c.rebindSessionRuntime(ctx, true)
	return nil
}

// NewSession starts a fresh conversation in the same project.
func (c *Controller) NewSession(ctx context.Context) error {
	c.mu.Lock()
	cwd := c.cwd
	c.mu.Unlock()
	return c.NewSessionInProject(ctx, cwd)
}

// NewSessionInProject starts a fresh conversation rooted at the explicitly
// selected desktop project, rebuilding every cwd-bound runtime component when
// the folder differs from the active Session.
func (c *Controller) NewSessionInProject(ctx context.Context, path string) error {
	target, err := canonicalProjectPath(path)
	if err != nil {
		return err
	}
	if c.hasUnsettledInput() {
		return ErrBusy
	}
	c.StopComputer()
	if err := c.persist(); err != nil {
		return fmt.Errorf("save current session: %w", err)
	}
	c.mu.Lock()
	currentProject := c.cwd
	store := c.store
	c.mu.Unlock()
	if !sameProjectPath(target, currentProject) {
		next, err := c.prepareProjectRuntime(ctx, target)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		header := session.SessionHeader{
			ID: session.NewID(now), CreatedAt: now, UpdatedAt: now,
			Model: next.live.Model, Provider: next.live.ProviderName,
			SystemPrompt: next.env.SysPrompt, BaseInstruction: next.env.Prompt.Base,
			AppendInstructions: next.env.Prompt.Appends, Cwd: next.env.Cwd,
		}
		lock, err := acquireSessionLock(store.Dir(), header.ID)
		if err != nil {
			closeProjectRuntime(next)
			return err
		}
		c.installProjectSession(ctx, next, header, nil, lock, false, session.WriterInfo{}, false)
		_ = c.SelectProject(target)
		return nil
	}
	return c.newSessionInCurrentProject(ctx, currentProject)
}

func (c *Controller) newSessionInCurrentProject(ctx context.Context, cwd string) error {
	now := time.Now().UTC()
	c.mu.Lock()
	header := session.SessionHeader{
		ID:                 session.NewID(now),
		CreatedAt:          now,
		UpdatedAt:          now,
		Model:              c.live.Model,
		Provider:           c.live.ProviderName,
		SystemPrompt:       c.opt.SysPrompt,
		BaseInstruction:    c.opt.Prompt.Base,
		AppendInstructions: c.opt.Prompt.Appends,
		Cwd:                cwd,
	}
	tools := c.opt.Tools
	c.mu.Unlock()
	if err := c.relock(header.ID); err != nil {
		return err
	}
	c.mu.Lock()
	c.header = header
	c.browser = agenttool.ConfigureBrowserSession(tools, header.ID)
	c.agentCtx = &agentcore.AgentContext{SystemPrompt: c.opt.SysPrompt, Tools: c.opt.Tools}
	c.items = nil
	c.index = map[string]int{}
	c.curLeaf = ""
	c.persisted = 0
	c.compacted = false
	c.approvedForSession = map[string]bool{}
	c.deniedForSession = map[string]bool{}
	c.answered = map[string]bool{}
	c.pending = nil
	c.title = "New session"
	c.status.SessionID = header.ID
	c.status.Title = c.title
	c.status.ContextUsed = 0
	c.hookDeps = run.HookDeps{SessionID: header.ID, ProjectDir: cwd, WarnLog: c.warnWriter()}
	c.dispatcher = nil
	c.mu.Unlock()
	agenttool.ClosePaneAgent(tools)
	if err := agenttool.ConfigurePaneAgent(ctx, tools, header.ID); err != nil {
		c.warnf("pane control registration unavailable: %v", err)
	}
	c.wireHooks(ctx, false)
	c.emitStatus()
	_ = c.SelectProject(cwd)
	return nil
}

// relock moves the writer claim from the current session to id. The old lock is
// released only after the new one is held, so a failure leaves the caller's
// current session usable.
func (c *Controller) relock(id string) error {
	c.mu.Lock()
	store, old := c.store, c.lock
	c.mu.Unlock()
	if store == nil {
		return errors.New("session is not open")
	}
	lk, err := acquireSessionLock(store.Dir(), id)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.lock = lk
	c.readOnly = false
	c.writerInfo = session.WriterInfo{}
	c.status.ReadOnly = false
	c.status.Writer = SessionWriterView{}
	c.mu.Unlock()
	old.release()
	return nil
}

func (c *Controller) reaccess(id string) error {
	c.mu.Lock()
	store, old := c.store, c.lock
	c.mu.Unlock()
	if store == nil {
		return errors.New("session is not open")
	}
	lk, readOnly, holder, err := acquireSessionAccess(store.Dir(), id)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.lock = lk
	c.readOnly, c.writerInfo = readOnly, holder
	c.status.ReadOnly, c.status.Writer = readOnly, writerView(holder)
	c.mu.Unlock()
	old.release()
	return nil
}

// adopt replaces the active session with header h and its entry path. The live
// model and provider are kept, matching the TUI and the CLI's resume rules.
func (c *Controller) adopt(h session.SessionHeader, entries []session.Entry) {
	msgs := make(agentcore.MessageList, len(entries))
	for i, e := range entries {
		msgs[i] = e.Message
	}
	c.mu.Lock()
	sysPrompt := cli.ResumeSystemPrompt(h, c.prompt)
	c.header = h
	c.browser = agenttool.ConfigureBrowserSession(c.opt.Tools, h.ID)
	c.agentCtx = &agentcore.AgentContext{SystemPrompt: sysPrompt, Messages: msgs, Tools: c.opt.Tools}
	c.persisted = len(msgs)
	c.compacted = false
	c.curLeaf = ""
	if len(entries) > 0 {
		c.curLeaf = entries[len(entries)-1].ID
	}
	c.live.RefreshContextWindow()
	c.items = nil
	c.index = map[string]int{}
	c.approvedForSession = map[string]bool{}
	c.deniedForSession = map[string]bool{}
	c.answered = map[string]bool{}
	c.pending = nil
	c.hookDeps = run.HookDeps{SessionID: h.ID, ProjectDir: c.cwd, WarnLog: c.warnWriter()}
	c.dispatcher = nil
	c.status.SessionID = h.ID
	c.status.ContextUsed = estimateTokens(msgs)
	c.status.ContextLimit = c.live.ContextWindow
	c.mu.Unlock()

	c.seed(msgs)
	c.mu.Lock()
	if strings.TrimSpace(h.DisplayName) != "" {
		c.title = h.DisplayName
	}
	c.status.Title = c.title
	c.mu.Unlock()
	c.emitStatus()
}

func (c *Controller) rebindSessionRuntime(ctx context.Context, resumed bool) {
	c.mu.Lock()
	tools := c.opt.Tools
	id := c.header.ID
	readOnly := c.readOnly
	c.browser = agenttool.ConfigureBrowserSession(tools, id)
	c.mu.Unlock()
	agenttool.ClosePaneAgent(tools)
	if readOnly {
		return
	}
	if err := agenttool.ConfigurePaneAgent(ctx, tools, id); err != nil {
		c.warnf("pane control registration unavailable: %v", err)
	}
	c.wireHooks(ctx, resumed)
}

// IsRunning reports whether a turn is in flight.
func (c *Controller) IsRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}

// hasUnsettledInput prevents an in-place session switch from moving queued
// operator input into a different conversation after a local frontend handoff.
func (c *Controller) hasUnsettledInput() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running || len(c.steerQueue) > 0 || len(c.steerDelivered) > 0
}

// SessionID returns the active session id.
func (c *Controller) SessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.header.ID
}

// Export writes the session as JSONL or Markdown.
func (c *Controller) Export(format, path string) error {
	c.mu.Lock()
	id := c.header.ID
	c.mu.Unlock()
	return c.ExportSession(id, format, path)
}

// RenameSession writes a display-name override. Renaming the active in-memory
// session works before its first message creates a file; the name is persisted
// with that first turn.
func (c *Controller) RenameSession(ctx context.Context, id, title string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id, title = strings.TrimSpace(id), strings.TrimSpace(title)
	c.mu.Lock()
	store, current := c.store, c.header.ID
	c.mu.Unlock()
	if id == current {
		c.mu.Lock()
		readOnly := c.readOnly
		c.mu.Unlock()
		if readOnly {
			return ErrReadOnly
		}
		if err := c.persist(); err != nil {
			return err
		}
		header, err := store.Rename(id, title)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		c.mu.Lock()
		c.header.DisplayName = title
		if err == nil {
			c.header = header
		}
		c.title = title
		if c.title == "" {
			if err == nil {
				c.title = sessionTitle(store, c.header)
			} else {
				c.title = "New session"
			}
		}
		c.status.Title = c.title
		c.mu.Unlock()
		c.emitStatus()
		return nil
	}
	return c.withInactiveSessionLock(id, func() error {
		_, err := store.Rename(id, title)
		return err
	})
}

// ArchiveSession hides or restores a non-current session.
func (c *Controller) ArchiveSession(ctx context.Context, id string, archived bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	c.mu.Lock()
	store, current := c.store, c.header.ID
	c.mu.Unlock()
	if id == current {
		return fmt.Errorf("cannot archive the current session")
	}
	return c.withInactiveSessionLock(id, func() error {
		_, err := store.SetArchived(id, archived)
		return err
	})
}

// DeleteSession permanently removes a non-current session.
func (c *Controller) DeleteSession(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	c.mu.Lock()
	store, current := c.store, c.header.ID
	c.mu.Unlock()
	if id == current {
		return fmt.Errorf("cannot delete the current session")
	}
	return c.withInactiveSessionLock(id, func() error { return store.Delete(id) })
}

// ExportSession writes any stored session as JSONL or Markdown.
func (c *Controller) ExportSession(id, format, path string) error {
	c.mu.Lock()
	store, current := c.store, c.header.ID
	c.mu.Unlock()
	write := func() error {
		switch strings.ToLower(strings.TrimSpace(format)) {
		case "", "jsonl":
			_, err := store.Export(id, path)
			return err
		case "markdown", "md":
			_, err := store.ExportShare(id, path, "markdown", session.ShareOptions{})
			return err
		}
		return fmt.Errorf("unsupported export format %q", format)
	}
	if id == current {
		if err := c.persist(); err != nil {
			return err
		}
		return write()
	}
	return c.withInactiveSessionLock(id, write)
}

func (c *Controller) withInactiveSessionLock(id string, fn func() error) error {
	if err := session.ValidateID(id); err != nil {
		return err
	}
	c.mu.Lock()
	store := c.store
	c.mu.Unlock()
	if store == nil {
		return errors.New("session is not open")
	}
	lk, err := acquireSessionLock(store.Dir(), id)
	if err != nil {
		return err
	}
	defer lk.release()
	return fn()
}
