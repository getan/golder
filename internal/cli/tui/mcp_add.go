package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/getan/golder/internal/extensions"
	"github.com/kballard/go-shellquote"
)

// mcpAddWizard owns sensitive, transient operator input separately from the
// composer, transcript, history and model-facing messages.
type mcpAddWizard struct {
	mode, phase    string
	input          textinput.Model
	drafts         []extensions.MCPDraft
	edit           *extensions.MCPEdit
	approvalOnly   bool
	replacedValues bool
	selected       int
	reviewOffset   int
	err            string
	note           string
}

type mcpAddDoneMsg struct {
	id          string
	saved       bool
	enabled     bool
	sessionOnly bool
	err         error
}

func (m Model) openMCPAddPicker() (tea.Model, tea.Cmd) {
	if m.running {
		m.transcript.addSystem("Finish the current run before changing MCP connections.")
		m.menu.close()
		m.relayout()
		return m, nil
	}
	rows := []pickItem{
		{Title: "Paste MCP JSON…", Detail: "recommended; pasted contents are hidden", Value: extensionValue("mcp-add-mode", "json")},
		{Title: "Enter remote HTTP URL…", Value: extensionValue("mcp-add-mode", "http")},
		{Title: "Enter local command…", Value: extensionValue("mcp-add-mode", "stdio")},
		{Title: "Back", Value: extensionValue("back")},
	}
	m.menu.openPickerDetailed(rows, "", "extensions")
	m.menu.pickHint = "↑↓ Select · Enter · Esc Close"
	m.relayout()
	return m, nil
}

func (m Model) beginMCPAdd(mode string) (tea.Model, tea.Cmd) {
	if m.running || m.opts.Extensions == nil {
		return m, nil
	}
	if mode != "json" && mode != "http" && mode != "stdio" {
		return m, nil
	}
	in := textinput.New()
	in.Prompt = "> "
	in.CharLimit = 1 << 20
	in.SetWidth(max(10, m.width-8))
	in.Focus()
	w := &mcpAddWizard{mode: mode, phase: "name", input: in}
	if mode == "json" {
		w.phase = "json"
		w.input.EchoMode = textinput.EchoPassword
	} else {
		w.input.SetValue("mcp")
	}
	m.mcpWizard = w
	m.menu.close()
	return m, nil
}

func (m Model) beginMCPEdit(id string) (tea.Model, tea.Cmd) {
	if m.running || m.opts.Extensions == nil {
		m.transcript.addSystem("Finish the current run before editing an MCP server.")
		m.menu.close()
		m.relayout()
		return m, nil
	}
	edit, err := m.opts.Extensions.PrepareMCPEdit(id)
	if err != nil {
		m.transcript.addSystem(extensions.DisplayText(err.Error(), 1024))
		return m, nil
	}
	cfg := edit.Config
	draft := extensions.MCPDraft{ID: id, Transport: cfg.Transport, Command: cfg.Command, Args: cfg.Args, URL: cfg.URL, Network: cfg.Network, Env: cfg.Env, Headers: cfg.Headers, OAuthClientID: cfg.OAuthClientID, OAuthIssuer: cfg.OAuthIssuer}
	in := textinput.New()
	in.Prompt, in.CharLimit = "> ", 1<<20
	in.SetWidth(max(10, m.width-8))
	in.Focus()
	if cfg.Transport == "http" {
		in.SetValue(cfg.URL)
	} else {
		in.SetValue(shellquote.Join(append([]string{cfg.Command}, cfg.Args...)...))
	}
	m.mcpWizard = &mcpAddWizard{mode: cfg.Transport, phase: "target", input: in, drafts: []extensions.MCPDraft{draft}, edit: &edit}
	m.menu.close()
	return m, nil
}

func (m Model) beginMCPApproval(id string) (tea.Model, tea.Cmd) {
	if m.running || m.opts.Extensions == nil {
		m.transcript.addSystem("Finish the current run before enabling an MCP server.")
		m.menu.close()
		m.relayout()
		return m, nil
	}
	edit, err := m.opts.Extensions.PrepareMCPEdit(id)
	if err != nil {
		m.transcript.addSystem(extensions.DisplayText(err.Error(), 1024))
		return m, nil
	}
	cfg := edit.Config
	draft := extensions.MCPDraft{ID: id, Transport: cfg.Transport, Command: cfg.Command, Args: cfg.Args, URL: cfg.URL, Network: cfg.Network, Env: cfg.Env, Headers: cfg.Headers, OAuthClientID: cfg.OAuthClientID, OAuthIssuer: cfg.OAuthIssuer}
	in := textinput.New()
	in.SetWidth(max(10, m.width-8))
	m.mcpWizard = &mcpAddWizard{mode: cfg.Transport, phase: "review", input: in, drafts: []extensions.MCPDraft{draft}, edit: &edit, approvalOnly: true}
	m.menu.close()
	return m, nil
}

func (w *mcpAddWizard) acceptJSON(raw string) {
	drafts, err := extensions.ParseMCPImport(raw)
	w.input.Reset()
	if err != nil {
		w.err = err.Error()
		return
	}
	w.drafts = drafts
	w.selected = 0
	w.reviewOffset = 0
	w.err = ""
	if len(drafts) == 1 {
		w.phase = "review"
	} else {
		w.phase = "select"
	}
}

func (w *mcpAddWizard) acceptReplacementJSON(raw string) {
	drafts, err := extensions.ParseMCPImport(raw)
	w.input.Reset()
	if err != nil {
		w.err = err.Error()
		return
	}
	if w.edit == nil || len(drafts) != 1 || (drafts[0].ID != "mcp" && drafts[0].ID != w.edit.ID) || drafts[0].Transport != w.edit.Config.Transport {
		w.err = "Paste one server with the same name and transport."
		return
	}
	draft := drafts[0]
	draft.ID, draft.Network = w.edit.ID, w.edit.Config.Network
	w.drafts, w.selected, w.reviewOffset = []extensions.MCPDraft{draft}, 0, 0
	w.err, w.phase = "", "review"
	w.replacedValues = true
}

func (m Model) mcpWizardPaste(content string) (tea.Model, tea.Cmd) {
	w := m.mcpWizard
	if w == nil {
		return m, nil
	}
	if w.phase == "json" {
		w.acceptJSON(content)
		return m, nil
	}
	if w.phase == "edit-json" {
		w.acceptReplacementJSON(content)
		return m, nil
	}
	if w.phase == "name" || w.phase == "target" || w.phase == "rename" || w.phase == "oauth-client" || w.phase == "oauth-issuer" || w.phase == "auth-token" {
		var cmd tea.Cmd
		w.input, cmd = w.input.Update(tea.PasteMsg{Content: content})
		return m, cmd
	}
	return m, nil
}

func (m Model) handleMCPWizardKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	w := m.mcpWizard
	if w.phase == "saving" {
		return m, nil
	}
	switch msg.String() {
	case "esc", "ctrl+c":
		m.mcpWizard = nil
		return m.openExtensions("mcp")
	case "ctrl+v", "super+v":
		return m, tea.ReadClipboard
	}
	if w.phase == "select" {
		switch msg.String() {
		case "up", "k":
			w.selected = (w.selected + len(w.drafts) - 1) % len(w.drafts)
		case "down", "j":
			w.selected = (w.selected + 1) % len(w.drafts)
		case "enter":
			w.phase, w.err, w.reviewOffset = "review", "", 0
		}
		return m, nil
	}
	if w.phase == "review" {
		switch msg.String() {
		case "down", "j":
			w.reviewOffset = min(w.reviewOffset+1, w.reviewMaxOffset(m.width, m.height))
			w.err = ""
		case "up", "k":
			w.reviewOffset = max(0, w.reviewOffset-1)
			w.err = ""
		case "pgdown":
			w.reviewOffset = min(w.reviewOffset+max(1, m.height-8), w.reviewMaxOffset(m.width, m.height))
			w.err = ""
		case "pgup":
			w.reviewOffset = max(0, w.reviewOffset-max(1, m.height-8))
			w.err = ""
		case "home":
			w.reviewOffset = 0
			w.err = ""
		case "end":
			w.reviewOffset = w.reviewMaxOffset(m.width, m.height)
			w.err = ""
		case "r":
			if w.edit == nil {
				w.phase = "rename"
				w.input.EchoMode = textinput.EchoNormal
				w.input.SetValue(w.drafts[w.selected].ID)
			}
		case "v":
			if w.edit != nil && !w.approvalOnly {
				w.phase, w.err = "edit-json", ""
				w.input.Reset()
				w.input.EchoMode = textinput.EchoPassword
			}
		case "n":
			if !w.approvalOnly && w.drafts[w.selected].Transport == "stdio" {
				w.drafts[w.selected].Network = !w.drafts[w.selected].Network
				w.reviewOffset = 0
			}
		case "c", "i":
			if !w.approvalOnly && w.drafts[w.selected].Transport == "http" {
				w.input.EchoMode = textinput.EchoNormal
				if msg.String() == "c" {
					w.phase = "oauth-client"
					w.input.SetValue(w.drafts[w.selected].OAuthClientID)
				} else {
					w.phase = "oauth-issuer"
					w.input.SetValue(w.drafts[w.selected].OAuthIssuer)
				}
			}
		case "s":
			if w.approvalOnly && (m.height > 0 && m.height < 10 || w.reviewOffset < w.reviewMaxOffset(m.width, m.height)) {
				w.err = "Scroll to the end and enlarge the terminal before approving."
				return m, nil
			}
			return m.saveMCPDraft(false)
		case "enter":
			if m.height > 0 && m.height < 10 {
				w.err = "Enlarge the terminal to review before approving."
				return m, nil
			}
			if w.reviewOffset < w.reviewMaxOffset(m.width, m.height) {
				w.err = "Scroll to the end before approving."
				return m, nil
			}
			return m.saveMCPDraft(true)
		case "backspace":
			if len(w.drafts) > 1 {
				w.phase = "select"
			}
		}
		return m, nil
	}
	if msg.String() == "enter" {
		value := strings.TrimSpace(w.input.Value())
		switch w.phase {
		case "json":
			w.acceptJSON(value)
		case "edit-json":
			w.acceptReplacementJSON(value)
		case "name":
			w.drafts = []extensions.MCPDraft{{ID: value, Transport: w.mode}}
			w.phase, w.err = "target", ""
			w.input.Reset()
		case "rename":
			d := w.drafts[w.selected]
			d.ID = value
			if err := d.Validate(); err != nil {
				w.err = err.Error()
				return m, nil
			}
			w.drafts[w.selected] = d
			w.phase, w.err, w.reviewOffset = "review", "", 0
			w.input.Reset()
		case "oauth-client", "oauth-issuer":
			d := w.drafts[w.selected]
			if w.phase == "oauth-client" {
				d.OAuthClientID = value
			} else {
				d.OAuthIssuer = value
			}
			if err := d.Validate(); err != nil {
				w.err = err.Error()
				return m, nil
			}
			w.drafts[w.selected] = d
			w.phase, w.err, w.reviewOffset = "review", "", 0
			w.input.Reset()
		case "auth-token":
			d := w.drafts[0]
			if value != "" {
				if !strings.HasPrefix(strings.ToLower(value), "bearer ") {
					value = "Bearer " + value
				}
				d.Headers = map[string]string{"Authorization": value}
			}
			if err := d.Validate(); err != nil {
				w.err = err.Error()
				return m, nil
			}
			w.drafts[0] = d
			w.phase, w.err, w.reviewOffset = "review", "", 0
			w.input.Reset()
		case "target":
			d := w.drafts[0]
			if w.mode == "http" {
				d.URL = value
			} else {
				argv, err := shellquote.Split(value)
				if err != nil || len(argv) == 0 {
					w.err = "Enter a valid command and arguments (no shell is executed)."
					return m, nil
				}
				d.Command, d.Args = argv[0], argv[1:]
			}
			if err := d.Validate(); err != nil {
				w.err = err.Error()
				return m, nil
			}
			w.drafts[0] = d
			w.input.Reset()
			if w.mode == "http" && w.edit == nil {
				w.phase, w.err = "auth-token", ""
				w.input.EchoMode = textinput.EchoPassword
			} else {
				w.phase, w.err, w.reviewOffset = "review", "", 0
			}
		}
		return m, nil
	}
	var cmd tea.Cmd
	w.input, cmd = w.input.Update(msg)
	return m, cmd
}

func (m Model) saveMCPDraft(enable bool) (tea.Model, tea.Cmd) {
	w := m.mcpWizard
	draft := w.drafts[w.selected]
	edit := w.edit
	approvalOnly := w.approvalOnly
	w.phase, w.err = "saving", ""
	manager := m.opts.Extensions
	return m, func() tea.Msg {
		result := mcpAddDoneMsg{id: draft.ID, enabled: enable, sessionOnly: approvalOnly && !enable}
		var err error
		if approvalOnly {
			err = manager.EnableReviewedMCP(*edit, enable)
		} else if edit != nil {
			err = manager.UpdateMCPTarget(*edit, draft)
		} else {
			err = manager.AddMCP(draft)
		}
		if err != nil {
			result.err = err
			return result
		}
		result.saved = true
		if approvalOnly && !enable {
			return result
		}
		if !enable {
			return result
		}
		ref := extensions.SourceRef{Kind: "mcp", ID: draft.ID}
		if !approvalOnly {
			reviewed, err := manager.PrepareMCPEdit(draft.ID)
			if err != nil {
				result.err = err
				return result
			}
			if err := manager.EnableReviewedMCP(reviewed, true); err != nil {
				result.err = err
				return result
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		result.err = manager.Connect(ctx, ref)
		return result
	}
}

func (m Model) finishMCPAdd(msg mcpAddDoneMsg) (tea.Model, tea.Cmd) {
	w := m.mcpWizard
	if w == nil || w.phase != "saving" || len(w.drafts) == 0 || w.drafts[w.selected].ID != msg.id {
		return m, nil
	}
	if msg.err != nil {
		w.err = extensions.DisplayText(msg.err.Error(), 1024)
		// Add may have succeeded while approval or connection failed. Never retry
		// a committed add or discard the actionable status.
		if msg.saved {
			w.err = "Saved; connection or approval failed: " + w.err + ". Esc returns to the server list."
			w.drafts[w.selected].Env, w.drafts[w.selected].Headers = nil, nil
			w.phase = "done"
		} else {
			w.phase = "review"
		}
		return m, nil
	}
	w.note = "Saved " + msg.id + " disabled."
	if msg.enabled {
		w.note = "Connected " + msg.id + "."
	} else if msg.sessionOnly {
		w.note = "Enabled " + msg.id + " for this session."
	}
	w.drafts = append(w.drafts[:w.selected], w.drafts[w.selected+1:]...)
	if len(w.drafts) == 0 {
		w.phase = "done"
	} else {
		w.selected = min(w.selected, len(w.drafts)-1)
		w.phase = "select"
	}
	return m, nil
}

func (w *mcpAddWizard) view(width, height int) string {
	if w.phase == "review" {
		return w.reviewView(width, height)
	}
	var b strings.Builder
	b.WriteString("\n  Add MCP server · operator only\n\n")
	switch w.phase {
	case "json":
		b.WriteString("  Paste MCP JSON. Contents are hidden and never sent to the model.\n")
		b.WriteString("  Bracketed paste or Ctrl+V accepts multi-line JSON.\n\n  ")
		b.WriteString(w.input.View())
		b.WriteString("\n\n  Enter parses typed JSON · Esc cancels")
	case "edit-json":
		b.WriteString("  Paste one replacement MCP JSON. Environment and headers will be replaced.\n  Contents are hidden and never sent to the model.\n\n  " + w.input.View() + "\n\n  Enter parses typed JSON · Esc cancels")
	case "name", "rename":
		b.WriteString("  Server name (lowercase letters, digits, - and _)\n\n  ")
		b.WriteString(w.input.View())
		b.WriteString("\n\n  Enter continues · Esc cancels")
	case "oauth-client":
		b.WriteString("  Public OAuth client ID (optional; not a client secret)\n\n  " + w.input.View() + "\n\n  Enter continues · Esc cancels")
	case "oauth-issuer":
		b.WriteString("  Expected OAuth issuer URL (optional; requires client ID)\n\n  " + w.input.View() + "\n\n  Enter continues · Esc cancels")
	case "auth-token":
		b.WriteString("  Bearer token (optional; leave empty for OAuth or no authentication)\n\n  " + w.input.View() + "\n\n  Input is hidden · Enter reviews · Esc cancels")
	case "target":
		if w.mode == "http" {
			b.WriteString("  Remote MCP URL (HTTPS; HTTP only for loopback)\n\n  ")
		} else {
			b.WriteString("  Program and arguments (parsed as argv; no shell is run)\n\n  ")
		}
		b.WriteString(w.input.View())
		b.WriteString("\n\n  Enter reviews · Esc cancels")
	case "select":
		b.WriteString("  Multiple servers found. Review and save each one separately:\n\n")
		for i, d := range w.drafts {
			mark := " "
			if i == w.selected {
				mark = "›"
			}
			fmt.Fprintf(&b, "  %s %s · %s\n", mark, d.ID, d.Transport)
		}
		b.WriteString("\n  ↑↓ select · Enter review · Esc cancels remaining")
	case "saving":
		b.WriteString("  Saving and, if approved, connecting. Please wait…")
	case "done":
		b.WriteString("  Esc returns to the MCP server list.")
	}
	if w.note != "" {
		b.WriteString("\n\n  " + extensions.DisplayText(w.note, 1024))
	}
	if w.err != "" {
		b.WriteString("\n\n  Error: " + extensions.DisplayText(w.err, 1024))
	}
	return b.String()
}

func (w *mcpAddWizard) reviewLines(width int) []string {
	if width <= 0 {
		width = 80
	}
	d := w.drafts[w.selected]
	var b strings.Builder
	fmt.Fprintf(&b, "Review %s\nType: %s\n", d.ID, d.Transport)
	if d.Transport == "http" {
		fmt.Fprintf(&b, "Endpoint: %q\nNetwork: yes\n", d.URL)
		if d.OAuthClientID == "" {
			b.WriteString("OAuth client: automatic registration\n")
		} else {
			fmt.Fprintf(&b, "OAuth client ID: %q\nOAuth issuer: %q\n", d.OAuthClientID, d.OAuthIssuer)
		}
	} else {
		fmt.Fprintf(&b, "Program: %q\nArguments: %q\nNetwork: %t", d.Command, d.Args, d.Network)
		if !w.approvalOnly {
			b.WriteString(" (n to toggle)")
		}
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "Environment keys: %v\nHeader keys: %v\n", slices.Sorted(maps.Keys(d.Env)), slices.Sorted(maps.Keys(d.Headers)))
	if w.edit == nil || w.replacedValues {
		b.WriteString("Values are hidden; literals are stored as credentials.\n")
	} else {
		b.WriteString("Values are hidden; existing configuration is retained.\n")
	}
	if w.edit != nil {
		c := w.edit.Config
		if d.ID == "lark" {
			fmt.Fprintf(&b, "Lark services: %q\nProject host: %q\nApp ID: %q\n", c.Env["LARK_MCP_DOMAINS"], c.Env["MEEGLE_HOST"], c.Env["LARK_MCP_APP_ID"])
		}
		capabilities, _ := json.MarshalIndent(map[string]any{
			"state_dir": c.StateDir, "dir": c.Dir, "startup_timeout": c.StartupTimeout, "call_timeout": c.CallTimeout,
			"env_passthrough": c.EnvPassthrough, "executables": c.Executables,
			"setup": c.Setup, "setup_variants": c.SetupVariants, "setup_origins": c.SetupOrigins,
			"skills": c.Skills, "allowed_tools": c.AllowedTools, "disallowed_tools": c.DisallowedTools,
			"confirm_tools": c.ConfirmTools, "aggregate_tools": c.AggregateTools,
		}, "", "  ")
		b.WriteString("Additional reviewed capabilities:\n" + string(capabilities) + "\n")
	}
	b.WriteString("URLs and command arguments are stored in plain text; do not place secrets in them.\n")
	b.WriteString("Connecting runs third-party code or contacts the endpoint. Business tool calls remain subject to approval.")
	if w.edit != nil && !w.approvalOnly {
		b.WriteString("\nPrevious approval will be revoked.")
	}
	return strings.Split(WrapToWidth(b.String(), max(4, width-4)), "\n")
}

func (w *mcpAddWizard) reviewMaxOffset(width, height int) int {
	return max(0, len(w.reviewLines(width))-w.reviewRows(height))
}

func (w *mcpAddWizard) reviewRows(height int) int {
	if height <= 0 {
		height = 24
	}
	reserved := 7
	if w.edit != nil && !w.approvalOnly {
		reserved++
	}
	return max(1, height-reserved)
}

func (w *mcpAddWizard) reviewView(width, height int) string {
	if height <= 0 {
		height = 24
	}
	lines := w.reviewLines(width)
	offset := min(w.reviewOffset, w.reviewMaxOffset(width, height))
	end := min(len(lines), offset+w.reviewRows(height))
	var b strings.Builder
	b.WriteString("  MCP review · operator only\n")
	if w.edit != nil && !w.approvalOnly {
		b.WriteString("  Previous approval will be revoked.\n")
	}
	for _, line := range lines[offset:end] {
		b.WriteString("  " + line + "\n")
	}
	fmt.Fprintf(&b, "  Lines %d-%d/%d · ↑↓/PgUp/PgDn scroll\n", offset+1, end, len(lines))
	if w.approvalOnly {
		b.WriteString("  Enter approve & connect · s approve for session · Esc\n")
	} else if w.edit == nil {
		b.WriteString("  Enter approve · s save · r rename · c/i OAuth · Esc\n")
	} else {
		b.WriteString("  Enter approve · s save · v replace JSON · c/i OAuth · Esc\n")
	}
	if w.err != "" {
		b.WriteString("  " + extensions.DisplayText(w.err, 1024))
	}
	return b.String()
}
