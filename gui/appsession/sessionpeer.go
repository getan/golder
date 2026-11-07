package appsession

import (
	"context"
	"errors"
	"strings"
)

// newPeer creates a separate project-rooted runtime rather than sharing tools,
// approvals, process leases or agent context with an in-flight conversation.
func (c *Controller) newPeer(ctx context.Context, resumeID, project string) (*Controller, error) {
	c.mu.Lock()
	if c.closed || c.store == nil || c.live == nil {
		c.mu.Unlock()
		return nil, ErrClosed
	}
	store, options, factory := c.store, c.opt, c.factory
	currentProject, approvedProject := c.cwd, c.approvedCwd
	c.mu.Unlock()
	if resumeID != "" {
		header, _, err := store.LoadEntries(resumeID)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(header.Cwd) != "" {
			project = header.Cwd
		}
	}
	if strings.TrimSpace(project) == "" {
		project = currentProject
	}
	project, err := canonicalProjectPath(project)
	if err != nil {
		return nil, err
	}
	if factory == nil {
		return nil, errors.New("this GUI build cannot create independent session runtimes")
	}
	next, err := c.prepareProjectRuntime(ctx, project)
	if err != nil {
		return nil, err
	}
	options.Model, options.ProviderName, options.Provider = next.live.Model, next.live.ProviderName, next.live.Provider
	options.BaseURL, options.Protocol, options.ThinkingLevel, options.MaxContext = next.live.BaseURL, next.live.Protocol, next.live.ThinkingLevel, next.live.MaxContext
	options.Tools, options.SysPrompt, options.Prompt = next.env.Tools, next.env.SysPrompt, next.env.Prompt
	options.Skills, options.Extensions = next.env.Skills, next.env.Extensions
	options.Permissions, options.ReviewNotes, options.Budget = next.env.Permissions, next.env.ReviewNotes, next.env.Budget
	options.Credentials, options.Memory = next.env.Credentials, next.env.Memory
	options.Cwd, options.Store, options.ResumeID, options.ProjectFactory = next.env.Cwd, store, resumeID, factory
	options.Approve = approvedProject != "" && sameProjectPath(approvedProject, project)
	peer := New(options)
	if err := peer.Open(ctx); err != nil {
		_ = peer.Close()
		return nil, err
	}
	return peer, nil
}
