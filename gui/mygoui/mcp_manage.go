package mygoui

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/getan/golder/gui/appsession"
	"github.com/getan/golder/internal/extensions"
	"github.com/kballard/go-shellquote"
)

type mcpRemoveState struct{ edit extensions.MCPEdit }

func (a *App) beginMCPEdit(id string) {
	if a.ctrl == nil || a.extensionBusy || a.status.Running {
		return
	}
	edit, err := a.ctrl.PrepareMCPEdit(id)
	if err != nil {
		a.extensionErr = err.Error()
		return
	}
	f := newMCPAddForm()
	f.edit, f.name = &edit, id
	if edit.Config.Transport == "http" {
		f.mode, f.target = "Remote URL", edit.Config.URL
	} else {
		f.mode = "Local command"
		f.target = shellquote.Join(append([]string{edit.Config.Command}, edit.Config.Args...)...)
	}
	a.mcpAdd = f
	a.mcpRemove = nil
}

func (a *App) confirmMCPRemove(id string) {
	if a.ctrl == nil || a.extensionBusy || a.status.Running {
		return
	}
	if a.mcpRemove == nil || a.mcpRemove.edit.ID != id {
		edit, err := a.ctrl.PrepareMCPEdit(id)
		if err != nil {
			a.extensionErr = err.Error()
			return
		}
		a.mcpRemove = &mcpRemoveState{edit: edit}
		a.extensionNote = "Confirm removal of mcp:" + id + ". Named credentials and local login state will be kept."
		return
	}
	edit := a.mcpRemove.edit
	a.mcpRemove = nil
	a.runExtensionOperation("Removing mcp:"+id+"…", func(ctx context.Context, ctrl *appsession.Controller) error {
		return ctrl.RemoveMCP(ctx, edit)
	})
}

func (a *App) mcpToolsView(c *ui.Context, id string) {
	if a.ctrl == nil {
		return
	}
	t := c.Theme()
	tools := a.ctrl.MCPTools(id)
	ui.Column(c).Padding(8, 12).Gap(4).Children(func() {
		ui.Text(c, "Tools · "+id).FontWeight(600)
		if len(tools) == 0 {
			ui.Text(c, "No tools discovered. Enable and test the server first.").FontSize(11).TextColor(t.TextMuted).Wrap()
		}
		for _, tool := range tools {
			ui.Text(c, extensions.DisplayText(tool.Spec.Name, 256)+" · "+extensions.DisplayText(tool.State, 64)).FontSize(11).Wrap()
			if tool.Spec.Description != "" {
				ui.Text(c, extensions.DisplayText(tool.Spec.Description, 512)).FontSize(10).TextColor(t.TextMuted).Wrap()
			}
		}
	})
}

func (a *App) mcpApprovalView(c *ui.Context) {
	if a.mcpApproval == nil {
		return
	}
	edit := a.mcpApproval.edit
	cfg := edit.Config
	t := c.Theme()
	ui.Column(c).Padding(12).Gap(6).Radius(9).Background(t.Surface).Border(1, t.Warning).Children(func() {
		ui.Text(c, "Review mcp:"+edit.ID).FontWeight(600)
		ui.Text(c, fmt.Sprintf("Transport: %s · Network: %t · Private state: %t", cfg.Transport, cfg.Network || cfg.Transport == "http", cfg.StateDir)).FontSize(11).Wrap()
		if cfg.Transport == "http" {
			ui.Text(c, fmt.Sprintf("Endpoint: %q\nOAuth client ID: %q\nOAuth issuer: %q", cfg.URL, cfg.OAuthClientID, cfg.OAuthIssuer)).FontSize(11).Wrap()
		} else {
			ui.Text(c, fmt.Sprintf("Program: %q\nArguments: %q", cfg.Command, cfg.Args)).FontSize(11).Wrap()
		}
		ui.Text(c, fmt.Sprintf("Environment keys: %v\nHeader keys: %v", slices.Sorted(maps.Keys(cfg.Env)), slices.Sorted(maps.Keys(cfg.Headers)))).FontSize(11).Wrap()
		capabilities, _ := json.MarshalIndent(map[string]any{
			"dir": cfg.Dir, "startup_timeout": cfg.StartupTimeout, "call_timeout": cfg.CallTimeout,
			"env_passthrough": cfg.EnvPassthrough, "executables": cfg.Executables,
			"setup": cfg.Setup, "setup_variants": cfg.SetupVariants, "setup_origins": cfg.SetupOrigins,
			"skills": cfg.Skills, "allowed_tools": cfg.AllowedTools, "disallowed_tools": cfg.DisallowedTools,
			"confirm_tools": cfg.ConfirmTools, "aggregate_tools": cfg.AggregateTools,
		}, "", "  ")
		ui.Text(c, string(capabilities)).FontSize(10).Wrap()
		ui.Text(c, "Connecting runs third-party code or contacts the endpoint. Business tool calls remain subject to approval.").FontSize(11).TextColor(t.Warning).Wrap()
		if subtlePrimaryButton(c, "Confirm enable", a.extensionBusy).Clicked() {
			a.mcpApproval = nil
			a.extensionSourceConfirm = ""
			a.runExtensionOperation("Enabling mcp:"+edit.ID+"…", func(_ context.Context, ctrl *appsession.Controller) error {
				return ctrl.EnableReviewedMCP(edit)
			})
		}
	})
}
