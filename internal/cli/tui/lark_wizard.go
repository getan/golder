package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/getan/golder/internal/extensions"
)

var larkWizardDomains = []string{"docs", "drive", "im", "sheets", "base"}

type larkSetupWizard struct {
	phase            string
	input            textinput.Model
	version          string
	pkg              extensions.ManagedPackage
	larkCLI          string
	projectCLI       string
	projectHost      string
	installedVersion string
	appID            string
	secret           string
	domains          []bool
	selected         int
	project          bool
	enabled          bool
	err              string
	cancel           context.CancelFunc
}

type larkWizardDoneMsg struct {
	step    string
	pkg     extensions.ManagedPackage
	cli     string
	enabled bool
	err     error
}

func (m Model) beginLarkWizard() (tea.Model, tea.Cmd) {
	if m.running || m.opts.Extensions == nil {
		m.transcript.addSystem("Finish the current run before configuring Lark.")
		m.menu.close()
		m.relayout()
		return m, nil
	}
	ref := extensions.SourceRef{Kind: "mcp", ID: "lark"}
	if m.opts.Extensions.HasSource(ref) {
		return m.extensionDetail(ref)
	}
	in := textinput.New()
	in.Prompt, in.CharLimit = "> ", 4096
	in.SetWidth(max(10, m.width-8))
	in.Focus()
	w := &larkSetupWizard{input: in, version: m.opts.Version, projectHost: "project.feishu.cn", domains: []bool{true, true, true, true, true}}
	if pkg, ok := extensions.InstalledManagedPackage("lark"); ok {
		w.installedVersion = pkg.Version
		release, err := extensions.LarkReleaseFor(m.opts.Version)
		if err != nil || release.Version == pkg.Version {
			w.pkg = pkg
		}
	}
	if cli, ok := extensions.DiscoverOfficialCLI(extensions.OfficialLarkCLI, ""); ok {
		w.larkCLI = cli.Executable
	}
	if cli, ok := extensions.DiscoverOfficialCLI(extensions.OfficialProjectCLI, ""); ok {
		w.projectCLI = cli.Executable
	}
	w.nextAfterDetection()
	m.larkWizard = w
	m.menu.close()
	return m, nil
}

func (w *larkSetupWizard) nextAfterDetection() {
	switch {
	case w.pkg.Executable == "":
		w.phase = "helper"
	case w.larkCLI == "":
		w.phase = "lark-cli"
	default:
		w.phase = "app-id"
		w.input.EchoMode = textinput.EchoNormal
		w.input.Reset()
	}
}

func (m Model) larkWizardPaste(content string) (tea.Model, tea.Cmd) {
	w := m.larkWizard
	if w == nil || w.phase != "app-id" && w.phase != "app-secret" && w.phase != "project-host" {
		return m, nil
	}
	var cmd tea.Cmd
	w.input, cmd = w.input.Update(tea.PasteMsg{Content: content})
	return m, cmd
}

func (m Model) handleLarkWizardKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	w := m.larkWizard
	if msg.String() == "esc" || msg.String() == "ctrl+c" {
		if w.cancel != nil {
			w.cancel()
			w.phase = "cancelling"
			return m, nil
		}
		m.larkWizard = nil
		return m.openExtensions("mcp")
	}
	if msg.String() == "ctrl+v" || msg.String() == "super+v" {
		if w.phase == "app-id" || w.phase == "app-secret" || w.phase == "project-host" {
			return m, tea.ReadClipboard
		}
		return m, nil
	}
	switch w.phase {
	case "helper":
		if msg.String() == "enter" {
			if _, err := extensions.LarkReleaseFor(w.version); err != nil {
				w.err = err.Error()
				return m, nil
			}
			return m.installLarkWizardDependency("helper")
		}
	case "lark-cli", "project-cli":
		if msg.String() == "enter" {
			return m.installLarkWizardDependency(w.phase)
		}
	case "app-id":
		if msg.String() == "enter" {
			w.appID = strings.TrimSpace(w.input.Value())
			if w.appID == "" {
				w.err = "Enter the App ID from your existing Lark application."
				return m, nil
			}
			w.phase, w.err = "app-secret", ""
			w.input.Reset()
			w.input.EchoMode = textinput.EchoPassword
			return m, nil
		}
	case "app-secret":
		if msg.String() == "enter" {
			w.secret = w.input.Value()
			w.input.Reset()
			if w.secret == "" || w.secret != strings.TrimSpace(w.secret) {
				w.secret = ""
				w.err = "Enter a valid App Secret."
				return m, nil
			}
			w.phase, w.err = "domains", ""
			return m, nil
		}
	case "domains":
		switch msg.String() {
		case "up", "k":
			w.selected = (w.selected + len(w.domains) - 1) % len(w.domains)
		case "down", "j":
			w.selected = (w.selected + 1) % len(w.domains)
		case "space":
			w.domains[w.selected] = !w.domains[w.selected]
		case "enter":
			if len(w.selectedDomains()) == 0 {
				w.err = "Select at least one Lark service."
			} else {
				w.phase, w.err = "project", ""
			}
		}
		return m, nil
	case "project":
		switch msg.String() {
		case "space", "p":
			w.project = !w.project
		case "enter":
			if w.project && w.projectCLI == "" {
				w.phase = "project-cli"
			} else if w.project {
				w.beginProjectHost()
			} else {
				w.phase = "review"
			}
		}
		return m, nil
	case "project-host":
		if msg.String() == "enter" {
			w.projectHost = strings.TrimSpace(w.input.Value())
			w.input.Reset()
			if w.projectHost == "" {
				w.projectHost = "project.feishu.cn"
			}
			w.phase, w.err = "review", ""
			return m, nil
		}
	case "review":
		switch msg.String() {
		case "h":
			if w.project {
				w.beginProjectHost()
			}
		case "s":
			return m.saveLarkWizard(false)
		case "enter":
			return m.saveLarkWizard(true)
		}
	case "done":
		if msg.String() == "enter" {
			enabled := w.enabled
			m.larkWizard = nil
			if enabled {
				return m.startExtensionSetup("mcp", []string{"setup", "lark"})
			}
			return m.extensionDetail(extensions.SourceRef{Kind: "mcp", ID: "lark"})
		}
	}
	if w.phase == "app-id" || w.phase == "app-secret" || w.phase == "project-host" {
		var cmd tea.Cmd
		w.input, cmd = w.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (w *larkSetupWizard) selectedDomains() []string {
	var selected []string
	for i, yes := range w.domains {
		if yes {
			selected = append(selected, larkWizardDomains[i])
		}
	}
	return selected
}

func (w *larkSetupWizard) beginProjectHost() {
	if w.projectHost == "" {
		w.projectHost = "project.feishu.cn"
	}
	w.phase = "project-host"
	w.input.EchoMode = textinput.EchoNormal
	w.input.SetValue(w.projectHost)
}

func (m Model) installLarkWizardDependency(step string) (tea.Model, tea.Cmd) {
	w := m.larkWizard
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	w.cancel, w.phase, w.err = cancel, "installing", ""
	version := w.version
	return m, func() tea.Msg {
		msg := larkWizardDoneMsg{step: step}
		if step == "helper" {
			msg.pkg, msg.err = extensions.InstallLarkRelease(ctx, version)
			return msg
		}
		kind := extensions.OfficialLarkCLI
		if step == "project-cli" {
			kind = extensions.OfficialProjectCLI
		}
		cli, err := extensions.NewOfficialCLIInstaller().Install(ctx, kind)
		msg.cli, msg.err = cli.Executable, err
		return msg
	}
}

func (m Model) saveLarkWizard(enable bool) (tea.Model, tea.Cmd) {
	w := m.larkWizard
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	w.cancel, w.phase, w.err = cancel, "saving", ""
	setup := extensions.LarkSetup{Package: w.pkg, LarkCLI: w.larkCLI, AppID: w.appID, AppSecret: w.secret, Domains: w.selectedDomains()}
	if w.project {
		setup.ProjectCLI = w.projectCLI
		setup.ProjectHost = w.projectHost
	}
	manager := m.opts.Extensions
	return m, func() tea.Msg {
		msg := larkWizardDoneMsg{step: "save", enabled: enable}
		if err := manager.AddLark(ctx, setup); err != nil {
			msg.err = err
			return msg
		}
		if !enable {
			return msg
		}
		if err := ctx.Err(); err != nil {
			msg.err = err
		} else {
			reviewed, err := manager.PrepareMCPEdit("lark")
			if err != nil {
				msg.err = err
			} else {
				msg.err = manager.EnableReviewedMCP(reviewed, true)
			}
		}
		return msg
	}
}

func (m Model) finishLarkWizardOperation(msg larkWizardDoneMsg) (tea.Model, tea.Cmd) {
	w := m.larkWizard
	if w == nil || w.cancel == nil {
		return m, nil
	}
	w.cancel()
	w.cancel = nil
	if msg.err != nil {
		w.err = extensions.DisplayText(msg.err.Error(), 1024)
		if msg.step == "save" && m.opts.Extensions.HasSource(extensions.SourceRef{Kind: "mcp", ID: "lark"}) {
			w.secret = ""
			w.err = "Lark was saved, but enabling failed: " + w.err + ". Return to the MCP list to review it."
			w.enabled, w.phase = false, "done"
		} else {
			w.phase = msg.step
			if msg.step == "save" {
				w.phase = "review"
			}
		}
		return m, nil
	}
	switch msg.step {
	case "helper":
		w.pkg = msg.pkg
		w.nextAfterDetection()
	case "lark-cli":
		w.larkCLI = msg.cli
		w.nextAfterDetection()
	case "project-cli":
		w.projectCLI = msg.cli
		w.beginProjectHost()
	case "save":
		w.secret, w.enabled, w.phase = "", msg.enabled, "done"
	}
	return m, nil
}

func (w *larkSetupWizard) view() string {
	var b strings.Builder
	b.WriteString("\n  Lark + Project setup · operator only\n\n")
	switch w.phase {
	case "helper":
		release, err := extensions.LarkReleaseFor(w.version)
		if err != nil {
			b.WriteString("  " + err.Error() + "\n  Install a matching release build or configure Lark in the GUI.\n")
		} else {
			if w.installedVersion != "" {
				fmt.Fprintf(&b, "  Installed helper: %s (not the current release).\n", extensions.DisplayText(w.installedVersion, 64))
			}
			fmt.Fprintf(&b, "  Install the matching verified Golder helper %s?\n  %s\n  SHA-256 is checked against the official release checksums.\n\n  Enter install · Esc cancel", release.Version, release.ArchiveURL)
		}
	case "lark-cli", "project-cli":
		kind := extensions.OfficialLarkCLI
		if w.phase == "project-cli" {
			kind = extensions.OfficialProjectCLI
		}
		spec := extensions.NewOfficialCLIInstaller().Specs[kind]
		fmt.Fprintf(&b, "  Install official %s %s?\n  %s\n  The pinned archive checksum will be verified.\n\n  Enter install · Esc cancel", kind, spec.Version, spec.ArchiveURL)
	case "app-id":
		b.WriteString("  Enter the App ID of your existing Lark application.\n\n  " + w.input.View() + "\n\n  Enter continues · Esc cancels")
	case "app-secret":
		b.WriteString("  Enter the App Secret. Input is masked and never sent to the model.\n\n  " + w.input.View() + "\n\n  Enter continues · Esc cancels")
	case "domains":
		b.WriteString("  Select Lark services (all selected by default):\n\n")
		for i, name := range larkWizardDomains {
			mark, check := " ", " "
			if i == w.selected {
				mark = "›"
			}
			if w.domains[i] {
				check = "x"
			}
			fmt.Fprintf(&b, "  %s [%s] %s\n", mark, check, name)
		}
		b.WriteString("\n  ↑↓ select · Space toggle · Enter continue · Esc cancel")
	case "project":
		fmt.Fprintf(&b, "  [%-1s] Include Lark Project (separate CLI and authorization)\n\n  Space toggles · Enter continues · Esc cancels", map[bool]string{true: "x", false: " "}[w.project])
	case "project-host":
		b.WriteString("  Project host (default project.feishu.cn)\n\n  " + w.input.View() + "\n\n  Enter continues · Esc cancels")
	case "review":
		fmt.Fprintf(&b, "  Helper: %s\n  Lark CLI: %s\n  App ID: %s\n  Services: %s\n  Network: enabled\n", extensions.DisplayText(w.pkg.Executable, 512), extensions.DisplayText(w.larkCLI, 512), extensions.DisplayText(w.appID, 256), strings.Join(w.selectedDomains(), ", "))
		if w.project {
			fmt.Fprintf(&b, "  Project CLI: %s\n  Project host: %s (h to edit)\n", extensions.DisplayText(w.projectCLI, 512), extensions.DisplayText(w.projectHost, 253))
		}
		b.WriteString("  App Secret: stored as a private named credential\n\n  Enter approve & enable · s save disabled · Esc cancel")
	case "installing":
		b.WriteString("  Downloading and verifying the selected component…\n\n  Esc cancels")
	case "saving":
		b.WriteString("  Saving the reviewed configuration…\n\n  Esc cancels")
	case "cancelling":
		b.WriteString("  Cancelling; waiting for cleanup…")
	case "done":
		if w.enabled {
			b.WriteString("  Lark is configured and enabled.\n  Enter starts Lark personal authorization · Esc closes")
		} else {
			b.WriteString("  Lark is configured and disabled.\n  Enter opens source details · Esc closes")
		}
	default:
		b.WriteString("  Setup is unavailable. Esc closes.")
	}
	if w.err != "" {
		b.WriteString("\n\n  Error: " + extensions.DisplayText(w.err, 1024))
	}
	return b.String()
}
