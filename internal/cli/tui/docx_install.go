package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/getan/golder/internal/cli/prompts"
	"github.com/getan/golder/internal/extensions"
)

var docxSourceRef = extensions.SourceRef{Kind: "mcp", ID: "docx"}

// docxInstallWizard is the TUI's explicit install/enable path for the
// first-party DOCX helper. The release download uses the same checksum and
// mirror fallback as the GUI; source approval remains a separate reviewed
// operation performed only after the helper is installed.
type docxInstallWizard struct {
	phase    string // confirm, installing, cancelling, done, error
	version  string
	progress string
	err      string
	enabled  bool
	cancel   context.CancelFunc
	events   <-chan tea.Msg
}

type docxInstallEventMsg struct {
	done bool
	text string
	err  error
}

func (m Model) beginDocxInstall() (tea.Model, tea.Cmd) {
	if m.opts.Extensions == nil {
		m.transcript.addSystem("Extensions unavailable.")
		return m, nil
	}
	if m.running {
		m.transcript.addSystem("Finish the current run before installing the DOCX helper.")
		m.menu.close()
		m.relayout()
		return m, nil
	}
	m.menu.close()
	m.docxWizard = &docxInstallWizard{phase: "confirm", version: m.opts.Version}
	m.relayout()
	return m, nil
}

func (m Model) handleDocxInstallKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	w := m.docxWizard
	if w == nil {
		return m, nil
	}
	switch msg.String() {
	case "esc", "ctrl+c":
		if w.cancel != nil {
			w.cancel()
			w.phase = "cancelling"
			return m, nil
		}
		m.docxWizard = nil
		m.relayout()
		return m, nil
	case "enter":
		switch w.phase {
		case "confirm", "error":
			return m.startDocxInstall()
		case "done":
			m.docxWizard = nil
			return m.extensionDetail(docxSourceRef)
		}
	}
	return m, nil
}

func (m Model) startDocxInstall() (tea.Model, tea.Cmd) {
	w := m.docxWizard
	if w == nil || m.opts.Extensions == nil {
		return m, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	events := make(chan tea.Msg, 8)
	w.cancel, w.events, w.phase, w.progress, w.err = cancel, events, "installing", "Checking the release asset…", ""
	manager, version := m.opts.Extensions, w.version
	go func() {
		defer close(events)
		send := func(text string) {
			select {
			case events <- docxInstallEventMsg{text: text}:
			case <-ctx.Done():
			}
		}
		done := func(err error) {
			select {
			case events <- docxInstallEventMsg{done: true, err: err}:
			case <-ctx.Done():
			}
		}

		pkg, err := extensions.InstallDocxReleaseWithProgress(ctx, version, func(p extensions.DocxDownloadProgress) {
			send(docxTUIProgress(p))
		})
		if err != nil {
			done(err)
			return
		}
		send("Writing the reviewed DOCX source…")
		cfg := extensions.SourceConfig{
			Enabled: false, Transport: "stdio", Command: pkg.Executable,
			Args: []string{"serve"}, AllowedTools: []string{"docx_read", "docx_create", "docx_edit", "docx_render"},
			CallTimeout: "30s", Network: false,
		}
		if err := manager.ReplaceSourceConfig(docxSourceRef, cfg); err != nil {
			done(err)
			return
		}
		send("Enabling the reviewed DOCX source…")
		reviewed, err := manager.PrepareMCPEdit(docxSourceRef.ID)
		if err != nil {
			done(err)
			return
		}
		done(manager.EnableReviewedMCP(reviewed, true))
	}()
	return m, waitDocxInstall(events)
}

func waitDocxInstall(events <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-events
		if !ok {
			return docxInstallEventMsg{done: true}
		}
		return msg
	}
}

func (m Model) finishDocxInstall(msg docxInstallEventMsg) (tea.Model, tea.Cmd) {
	w := m.docxWizard
	if w == nil {
		return m, nil
	}
	if !msg.done {
		w.progress = msg.text
		w.phase = "installing"
		return m, waitDocxInstall(w.events)
	}
	if w.cancel != nil {
		w.cancel()
		w.cancel = nil
	}
	if msg.err != nil {
		w.err = extensions.DisplayText(msg.err.Error(), 1024)
		w.phase = "error"
		return m, nil
	}
	w.enabled, w.phase = true, "done"
	prompts.SyncExtensionSkills(m.slash, m.opts.Extensions)
	m.transcript.addSystem("DOCX helper installed and the reviewed MCP source is enabled.")
	return m, nil
}

func docxTUIProgress(p extensions.DocxDownloadProgress) string {
	source := strings.TrimSpace(p.Source)
	attempts := ""
	if p.TotalAttempts > 1 {
		attempts = fmt.Sprintf(" · source %d/%d", p.Attempt, p.TotalAttempts)
	}
	switch p.Stage {
	case "checking":
		return "Checking the release asset…"
	case "checksums":
		return "Downloading the SHA-256 manifest…" + attempts
	case "downloading":
		if p.Total > 0 {
			return fmt.Sprintf("Downloading DOCX helper… %s / %s · %s%s",
				docxBytes(p.Downloaded), docxBytes(p.Total), source, attempts)
		}
		return fmt.Sprintf("Downloading DOCX helper… %s · %s%s", docxBytes(p.Downloaded), source, attempts)
	case "verifying":
		return "Verifying the release checksum and archive bounds…"
	case "extracting":
		return "Extracting the verified helper…"
	case "installing":
		return "Installing the helper into the managed package store…"
	case "complete":
		return "Download and verification complete."
	default:
		return "DOCX helper: " + p.Stage
	}
}

func docxBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func (w *docxInstallWizard) view() string {
	if w == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n  DOCX helper · operator only\n\n")
	switch w.phase {
	case "confirm":
		release, err := extensions.DocxReleaseFor(w.version)
		if err != nil {
			b.WriteString("  " + err.Error() + "\n  Install a matching release build or use the GUI.\n")
		} else {
			fmt.Fprintf(&b, "  Install the verified DOCX helper %s?\n  %s\n\n  Enter install & enable · Esc cancel", release.Version, release.ArchiveURL)
		}
	case "installing":
		b.WriteString("  " + extensions.DisplayText(w.progress, 512) + "\n\n  Esc cancels")
	case "cancelling":
		b.WriteString("  Cancelling; waiting for cleanup…")
	case "done":
		b.WriteString("  DOCX is installed and enabled. Enter opens the MCP source · Esc closes.")
	case "error":
		b.WriteString("  Installation failed. Enter retries · Esc closes.")
	}
	if w.err != "" {
		b.WriteString("\n\n  Error: " + extensions.DisplayText(w.err, 1024))
	}
	return b.String()
}
