package appsession

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/getan/golder/internal/extensions"
	"github.com/getan/golder/internal/mcpauth"
)

// beginMCPMutation makes checking the run state and reserving the manager one
// operation. A new turn cannot race an in-flight source replacement.
func (c *Controller) beginMCPMutation() (*extensions.Manager, func(), error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, nil, ErrClosed
	}
	if c.running || c.privateAuthorization || c.extensionMutation {
		return nil, nil, ErrBusy
	}
	if c.opt.Extensions == nil {
		return nil, nil, errors.New("extension manager unavailable")
	}
	c.extensionMutation = true
	var once sync.Once
	return c.opt.Extensions, func() {
		once.Do(func() {
			c.mu.Lock()
			c.extensionMutation = false
			c.mu.Unlock()
		})
	}, nil
}

// AddMCP registers an operator-reviewed source. Saving disabled never starts
// code or connects. The optional approval is specific to this exact source;
// business tool calls still go through their normal permission gates.
func (c *Controller) AddMCP(ctx context.Context, draft extensions.MCPDraft, enable bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	manager, release, err := c.beginMCPMutation()
	if err != nil {
		return err
	}
	defer release()
	if err := manager.AddMCP(draft); err != nil {
		return err
	}
	if !enable {
		return nil
	}
	return approveAndConnectMCP(ctx, manager, draft.ID)
}

func approveAndConnectMCP(ctx context.Context, manager *extensions.Manager, id string) error {
	ref := extensions.SourceRef{Kind: "mcp", ID: id}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("MCP saved disabled: %w", err)
	}
	reviewed, err := manager.PrepareMCPEdit(id)
	if err != nil {
		return fmt.Errorf("MCP saved disabled: %w", err)
	}
	if err := manager.EnableReviewedMCP(reviewed, true); err != nil {
		return fmt.Errorf("MCP saved but could not be enabled: %w", err)
	}
	if err := manager.Connect(ctx, ref); err != nil {
		return fmt.Errorf("MCP saved and enabled but connection failed: %w", err)
	}
	return nil
}

// PrepareMCPEdit obtains an operator-only snapshot for a subsequent edit or
// removal. Its configuration may contain legacy secrets and must not be logged.
func (c *Controller) PrepareMCPEdit(id string) (extensions.MCPEdit, error) {
	c.mu.Lock()
	manager := c.opt.Extensions
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return extensions.MCPEdit{}, ErrClosed
	}
	if manager == nil {
		return extensions.MCPEdit{}, errors.New("extension manager unavailable")
	}
	return manager.PrepareMCPEdit(id)
}

func (c *Controller) EnableReviewedMCP(edit extensions.MCPEdit) error {
	manager, release, err := c.beginMCPMutation()
	if err != nil {
		return err
	}
	defer release()
	return manager.EnableReviewedMCP(edit, true)
}

func (c *Controller) UpdateMCP(ctx context.Context, edit extensions.MCPEdit, draft extensions.MCPDraft, enable bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	manager, release, err := c.beginMCPMutation()
	if err != nil {
		return err
	}
	defer release()
	if err := manager.UpdateMCPTarget(edit, draft); err != nil {
		return err
	}
	if !enable {
		return nil
	}
	return approveAndConnectMCP(ctx, manager, draft.ID)
}

func (c *Controller) RemoveMCP(ctx context.Context, edit extensions.MCPEdit) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	manager, release, err := c.beginMCPMutation()
	if err != nil {
		return err
	}
	defer release()
	return manager.RemoveMCP(edit)
}

// MCPTools reads the existing safe catalog without connecting to the server.
func (c *Controller) MCPTools(id string) []extensions.ToolView {
	c.mu.Lock()
	manager := c.opt.Extensions
	c.mu.Unlock()
	if manager == nil {
		return nil
	}
	if source, ok := manager.Source(extensions.SourceRef{Kind: "mcp", ID: id}); ok {
		return source.Tools
	}
	return nil
}

// AuthorizeMCP is available only while the GUI owns its private authorization
// guard, so no agent turn in this GUI can run alongside the login page.
func (c *Controller) AuthorizeMCP(ctx context.Context, id string, open mcpauth.Open) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	if c.running || !c.privateAuthorization {
		c.mu.Unlock()
		return errors.New("private authorization must be active before OAuth begins")
	}
	manager := c.opt.Extensions
	c.mu.Unlock()
	if manager == nil {
		return errors.New("extension manager unavailable")
	}
	ref := extensions.SourceRef{Kind: "mcp", ID: id}
	if err := manager.Disconnect(ref); err != nil {
		return err
	}
	return manager.AuthorizeMCP(ctx, ref, open)
}
