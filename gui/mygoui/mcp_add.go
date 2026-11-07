package mygoui

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/getan/golder/internal/extensions"
	"github.com/kballard/go-shellquote"
)

// The form and imported drafts are operator-only and may hold credentials.
// They must never be added to conversation items or ordinary logs.
type mcpAddForm struct {
	mode, phase    string
	name, target   string
	authToken      string
	drafts         []extensions.MCPDraft
	edit           *extensions.MCPEdit
	replacedValues bool
	selection      string
	err, note      string
	busy           bool
}

func newMCPAddForm() *mcpAddForm {
	return &mcpAddForm{mode: "Paste JSON", phase: "input", name: "mcp"}
}

func (f *mcpAddForm) parseClipboard(raw string) {
	drafts, err := extensions.ParseMCPImport(raw)
	if err != nil {
		f.err = err.Error()
		return
	}
	f.drafts, f.selection = drafts, drafts[0].ID
	f.err, f.phase = "", "review"
}

func (f *mcpAddForm) parseReplacement(raw string) {
	drafts, err := extensions.ParseMCPImport(raw)
	if err != nil {
		f.err = err.Error()
		return
	}
	if f.edit == nil || len(drafts) != 1 || (drafts[0].ID != "mcp" && drafts[0].ID != f.edit.ID) || drafts[0].Transport != f.edit.Config.Transport {
		f.err = "Paste one server with the same name and transport."
		return
	}
	draft := drafts[0]
	draft.ID, draft.Network = f.edit.ID, f.edit.Config.Network
	f.drafts, f.selection, f.err, f.phase = []extensions.MCPDraft{draft}, draft.ID, "", "review"
	f.replacedValues = true
}

func (f *mcpAddForm) prepareManual() {
	d := extensions.MCPDraft{ID: strings.TrimSpace(f.name)}
	if f.edit != nil {
		d.Env, d.Headers, d.Network = f.edit.Config.Env, f.edit.Config.Headers, f.edit.Config.Network
		d.OAuthClientID, d.OAuthIssuer = f.edit.Config.OAuthClientID, f.edit.Config.OAuthIssuer
	}
	if f.mode == "Remote URL" {
		d.Transport, d.URL = "http", strings.TrimSpace(f.target)
		if f.edit == nil && strings.TrimSpace(f.authToken) != "" {
			token := strings.TrimSpace(f.authToken)
			if !strings.HasPrefix(strings.ToLower(token), "bearer ") {
				token = "Bearer " + token
			}
			d.Headers = map[string]string{"Authorization": token}
		}
	} else if f.mode == "Local command" {
		d.Transport = "stdio"
		argv, err := shellquote.Split(strings.TrimSpace(f.target))
		if err != nil || len(argv) == 0 {
			f.err = "Enter a valid program and arguments; no shell is run."
			return
		}
		d.Command, d.Args = argv[0], argv[1:]
	} else {
		f.err = "Choose an input method."
		return
	}
	if err := d.Validate(); err != nil {
		f.err = err.Error()
		return
	}
	f.drafts, f.selection, f.err, f.phase = []extensions.MCPDraft{d}, d.ID, "", "review"
	f.authToken = ""
}

func (f *mcpAddForm) current() *extensions.MCPDraft {
	for i := range f.drafts {
		if f.drafts[i].ID == f.selection {
			return &f.drafts[i]
		}
	}
	return nil
}

func (a *App) saveMCPAdd(enable bool) {
	f := a.mcpAdd
	if f == nil || f.busy || f.current() == nil {
		return
	}
	draft := *f.current()
	if err := draft.Validate(); err != nil {
		f.err = err.Error()
		return
	}
	f.busy, f.err, f.note = true, "", "Saving…"
	win, ctrl := a.win, a.ctrl
	edit := f.edit
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var err error
		if edit != nil {
			err = ctrl.UpdateMCP(ctx, *edit, draft, enable)
		} else {
			err = ctrl.AddMCP(ctx, draft, enable)
		}
		if win == nil {
			return
		}
		win.Update(func() {
			if a.ctrl != ctrl || a.mcpAdd != f {
				return
			}
			f.busy = false
			if err != nil {
				f.err, f.note = err.Error(), ""
				if strings.HasPrefix(f.err, "MCP saved") {
					f.drafts = nil
					f.selection, f.phase = "", "done"
					f.note = "The source was saved. Close this panel, then use Authorize or Test below."
				}
				a.loadExtensionSettings()
				return
			}
			f.note = "Saved " + draft.ID + " disabled."
			if enable {
				f.note = "Connected " + draft.ID + "."
			}
			for i := range f.drafts {
				if f.drafts[i].ID == draft.ID {
					f.drafts = append(f.drafts[:i], f.drafts[i+1:]...)
					break
				}
			}
			if len(f.drafts) == 0 {
				f.selection, f.phase = "", "done"
			} else {
				f.selection = f.drafts[0].ID
			}
			a.loadExtensionSettings()
		})
	}()
}

func (a *App) mcpAddView(c *ui.Context) {
	f := a.mcpAdd
	if f == nil {
		return
	}
	t := c.Theme()
	ui.Column(c).Padding(14).Gap(10).Radius(12).Background(t.Surface).Border(1, t.Border).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			title := "Add MCP server"
			if f.edit != nil {
				title = "Edit MCP server"
			}
			ui.Text(c, title).FontSize(15).FontWeight(600)
			ui.Spacer(c)
			if subtleButton(c, "Close", f.busy).Clicked() {
				a.mcpAdd = nil
			}
		})
		if f.phase == "input" {
			ui.Field(c, "Connection", func() {
				ui.Select(c, &f.mode, []string{"Paste JSON", "Remote URL", "Local command"}).Disabled(f.edit != nil).FillWidth()
			})
			if f.mode == "Paste JSON" {
				ui.Text(c, "Copy an MCP JSON configuration, then import it from the clipboard. Contents are not displayed or sent to the model.").FontSize(12).TextColor(t.TextMuted).Wrap()
				if subtlePrimaryButton(c, "Paste and review", f.busy).Clicked() {
					f.parseClipboard(mygo.Clipboard.ReadText())
				}
			} else {
				ui.Field(c, "Name", func() { ui.TextInput(c, &f.name).Disabled(f.edit != nil).FillWidth().Placeholder("mcp") })
				label, placeholder := "URL", "https://example.com/mcp"
				if f.mode == "Local command" {
					label, placeholder = "Program and arguments", "npx -y example-mcp-server"
				}
				ui.Field(c, label, func() { ui.TextInput(c, &f.target).FillWidth().Placeholder(placeholder) })
				if f.mode == "Remote URL" && f.edit == nil {
					ui.Field(c, "Bearer token (optional)", func() {
						ui.TextInput(c, &f.authToken).Password().FillWidth().Placeholder("Leave blank for OAuth or no authentication")
					})
				}
				if subtlePrimaryButton(c, "Review", f.busy).Clicked() {
					f.prepareManual()
				}
			}
		} else if f.phase == "review" {
			names := make([]string, 0, len(f.drafts))
			for _, draft := range f.drafts {
				names = append(names, draft.ID)
			}
			if len(names) > 1 {
				ui.Field(c, "Server to review", func() { ui.Select(c, &f.selection, names).Disabled(f.busy).FillWidth() })
			}
			if d := f.current(); d != nil {
				ui.Field(c, "Source name", func() {
					if ui.TextInput(c, &d.ID).Disabled(f.busy || f.edit != nil).FillWidth().Changed() {
						f.selection = d.ID
					}
				})
				ui.Text(c, "Type: "+d.Transport).FontWeight(600)
				if d.Transport == "http" {
					ui.Text(c, fmt.Sprintf("Endpoint: %q", d.URL)).FontSize(12).Wrap()
					ui.Text(c, "Network: yes").FontSize(12)
					ui.Field(c, "Public OAuth client ID (optional)", func() {
						ui.TextInput(c, &d.OAuthClientID).Disabled(f.busy).FillWidth().Placeholder("Automatic registration when blank")
					})
					ui.Field(c, "Expected OAuth issuer (optional)", func() {
						ui.TextInput(c, &d.OAuthIssuer).Disabled(f.busy).FillWidth().Placeholder("https://auth.example.com")
					})
				} else {
					ui.Text(c, fmt.Sprintf("Program: %q", d.Command)).FontSize(12).Wrap()
					ui.Text(c, fmt.Sprintf("Arguments: %q", d.Args)).FontSize(12).Wrap()
					ui.Checkbox(c, &d.Network, "Allow network access for this local server").Disabled(f.busy)
				}
				ui.Text(c, fmt.Sprintf("Environment keys: %v · Header keys: %v", slices.Sorted(maps.Keys(d.Env)), slices.Sorted(maps.Keys(d.Headers)))).FontSize(11).TextColor(t.TextMuted).Wrap()
				if f.edit == nil || f.replacedValues {
					ui.Text(c, "Values are hidden; literals are stored as credentials.").FontSize(11).TextColor(t.TextMuted).Wrap()
				} else {
					ui.Text(c, "Values are hidden; existing configuration is retained.").FontSize(11).TextColor(t.TextMuted).Wrap()
				}
				ui.Text(c, "URLs and command arguments are stored in plain text; do not place secrets in them.").FontSize(11).TextColor(t.Warning).Wrap()
				if f.edit != nil {
					ui.Text(c, "Saving this edit revokes the previous approval.").FontSize(11).TextColor(t.Warning).Wrap()
					ui.Text(c, "A replacement JSON changes the environment and headers; unlisted values are removed.").FontSize(11).TextColor(t.TextMuted).Wrap()
					if subtleButton(c, "Paste replacement JSON", f.busy).Clicked() {
						f.parseReplacement(mygo.Clipboard.ReadText())
					}
				}
				ui.Text(c, "Connecting runs third-party code or contacts the endpoint. Business tool calls still require approval.").FontSize(11).TextColor(t.Warning).Wrap()
				ui.Row(c).Gap(8).Wrap().Children(func() {
					if subtleButton(c, "Save disabled", f.busy).Clicked() {
						a.saveMCPAdd(false)
					}
					if subtlePrimaryButton(c, "Approve & connect", f.busy).Clicked() {
						a.saveMCPAdd(true)
					}
				})
			}
		} else if f.phase == "done" {
			ui.Text(c, "The source is available in the list below.").FontSize(12)
		}
		if f.note != "" {
			ui.Text(c, extensions.DisplayText(f.note, 1024)).FontSize(11).TextColor(t.TextMuted).Wrap()
		}
		if f.err != "" {
			ui.Text(c, extensions.DisplayText(f.err, 1024)).FontSize(11).TextColor(t.Danger).Wrap()
		}
	})
}
