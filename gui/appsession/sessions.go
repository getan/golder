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
	"github.com/getan/golder/internal/cli"
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
	if err := c.relock(id); err != nil {
		return err
	}
	c.adopt(h, entries)
	return nil
}

// NewSession starts a fresh conversation in the same project.
func (c *Controller) NewSession(ctx context.Context) error {
	if c.IsRunning() {
		return ErrBusy
	}
	if err := c.persist(); err != nil {
		return fmt.Errorf("save current session: %w", err)
	}
	now := time.Now().UTC()
	header := session.SessionHeader{
		ID:                 session.NewID(now),
		CreatedAt:          now,
		UpdatedAt:          now,
		Model:              c.live.Model,
		Provider:           c.live.ProviderName,
		SystemPrompt:       c.opt.SysPrompt,
		BaseInstruction:    c.opt.Prompt.Base,
		AppendInstructions: c.opt.Prompt.Appends,
		Cwd:                c.cwd,
	}
	if err := c.relock(header.ID); err != nil {
		return err
	}
	c.mu.Lock()
	c.header = header
	c.agentCtx = &agentcore.AgentContext{SystemPrompt: c.opt.SysPrompt, Tools: c.opt.Tools}
	c.items = nil
	c.index = map[string]int{}
	c.curLeaf = ""
	c.persisted = 0
	c.compacted = false
	c.title = "New session"
	c.status.SessionID = header.ID
	c.status.Title = c.title
	c.status.ContextUsed = 0
	c.hookDeps.SessionID = header.ID
	c.mu.Unlock()
	c.emitStatus()
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
	c.hookDeps.SessionID = h.ID
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

// IsRunning reports whether a turn is in flight.
func (c *Controller) IsRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
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

// OpenProject notes the project directory for display. The process never
// chdir's: tools stay rooted where the app started, matching the CLI.
func (c *Controller) OpenProject(path string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	c.mu.Lock()
	c.opt.Cwd = path
	c.mu.Unlock()
}

// workingDir is the process working directory, used by the command to stamp a
// new controller.
func workingDir() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}
