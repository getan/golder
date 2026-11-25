package tui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/getan/golder/internal/extensions"
)

// These seams keep the update flow testable without network access or a live
// manager; production uses the release downloader and the shared rebind.
var (
	installLarkRelease = extensions.InstallLarkRelease
	rebindLarkSource   = extensions.RebindLarkSource
	larkReleaseFor     = extensions.LarkReleaseFor
)

type larkHelperUpdate struct {
	release     extensions.LarkRelease
	hostVersion string
	current     string
	phase       string // confirm, installing, failed, done
	err         string
	cancel      context.CancelFunc
}

type larkHelperDoneMsg struct {
	pkg extensions.ManagedPackage
	err error
}

type larkHelperInfo struct {
	ActiveVersion   string
	ExpectedVersion string
	Release         extensions.LarkRelease
	UpdateAvailable bool
}

// larkHelperInfo reports the helper the configured source runs and the helper
// the running Golder expects. ok is false for an unmanaged command, and the
// update is never offered for a dev build or an unavailable release.
func (m Model) larkHelperInfo(cfg extensions.SourceConfig) (larkHelperInfo, bool) {
	pkg, managed := extensions.ManagedPackageForExecutable("lark", cfg.Command)
	if !managed {
		return larkHelperInfo{}, false
	}
	info := larkHelperInfo{ActiveVersion: pkg.Version}
	release, err := larkReleaseFor(m.opts.Version)
	if err != nil || release.Version == "" || release.Version == "dev" || release.Version == "unknown" {
		return info, true
	}
	info.ExpectedVersion, info.Release = release.Version, release
	info.UpdateAvailable = release.Version != pkg.Version
	return info, true
}

// larkHelperGate refuses approval, authorization and connection tests while the
// running build expects different helper bytes. A dev build or an unavailable
// release only warns instead of blocking, because there may be nothing to
// update to and a hard block would lock Lark out entirely.
func (m Model) larkHelperGate() (allow bool, message string) {
	active, ok := extensions.LarkActiveHelperVersion(m.opts.Extensions)
	if !ok {
		return true, ""
	}
	expected := extensions.LarkHelperVersion()
	if active == expected {
		return true, ""
	}
	if _, err := larkReleaseFor(m.opts.Version); err != nil {
		return true, fmt.Sprintf("Lark helper %s differs from %s; no installable release, approving with a warning.", active, expected)
	}
	return false, fmt.Sprintf("Lark helper %s differs from %s; update it first from the Lark detail page.", active, expected)
}

// cancelLarkUpdate drops the dialog and stops a download that is still running.
// The installed package is only replaced after verification, so an interrupted
// update leaves the existing helper and source untouched.
func (m *Model) cancelLarkUpdate() {
	if m.larkUpdate == nil {
		return
	}
	if m.larkUpdate.cancel != nil {
		m.larkUpdate.cancel()
	}
	m.larkUpdate = nil
}

func (m Model) confirmLarkHelperUpdate() (tea.Model, tea.Cmd) {
	if m.running || m.opts.Extensions == nil {
		m.transcript.addSystem("Finish the current run before updating the Lark helper.")
		m.menu.close()
		m.relayout()
		return m, nil
	}
	cfg, err := m.opts.Extensions.Config(extensions.LarkSourceRef)
	if err != nil {
		m.transcript.addSystem(extensions.DisplayText(err.Error(), 1024))
		m.menu.close()
		m.relayout()
		return m, nil
	}
	helper, ok := m.larkHelperInfo(cfg)
	if !ok || !helper.UpdateAvailable {
		return m.extensionDetail(extensions.LarkSourceRef)
	}
	m.larkUpdate = &larkHelperUpdate{release: helper.Release, hostVersion: m.opts.Version, current: helper.ActiveVersion, phase: "confirm"}
	info := []pickInfoLine{
		{Label: "Helper", Value: helper.ActiveVersion + " → " + helper.Release.Version},
		{Label: "Download", Value: helper.Release.ArchiveURL},
		{Label: "Checksum", Value: "SHA-256 from " + helper.Release.ChecksumURL},
		{Label: "Effect", Value: "The source is disabled and must be reviewed again; credentials and login state are kept."},
	}
	m.menu.openPickerDetailed([]pickItem{
		{Title: "Cancel", Value: extensionValue("back"), Info: info},
		{Title: "Download and update", StatusTone: pickWarn, Value: extensionValue("lark-helper-confirm"), Info: info},
	}, "", "extensions")
	m.menu.pickHint = "↑↓ Select · Enter · Esc Close"
	m.relayout()
	return m, nil
}

func (m Model) updateLarkHelperConfirmed() (tea.Model, tea.Cmd) {
	u := m.larkUpdate
	if u == nil || u.phase == "installing" {
		return m, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	u.cancel, u.phase, u.err = cancel, "installing", ""
	// InstallLarkRelease resolves the host release that carries the helper;
	// u.release.Version is the helper's own version, not the host tag.
	version := u.hostVersion
	manager := m.opts.Extensions
	m = m.showLarkUpdate()
	return m, func() tea.Msg {
		pkg, err := installLarkRelease(ctx, version)
		if err == nil {
			err = rebindLarkSource(manager, pkg)
		}
		return larkHelperDoneMsg{pkg: pkg, err: err}
	}
}

func (m Model) finishLarkHelperUpdate(msg larkHelperDoneMsg) (tea.Model, tea.Cmd) {
	u := m.larkUpdate
	if u == nil || u.phase != "installing" {
		return m, nil
	}
	if u.cancel != nil {
		u.cancel()
		u.cancel = nil
	}
	if msg.err != nil {
		u.phase = "failed"
		u.err = extensions.DisplayText(msg.err.Error(), 1024)
		return m.showLarkUpdate(), nil
	}
	u.phase, u.current = "done", msg.pkg.Version
	return m.showLarkUpdate(), nil
}

// showLarkUpdate renders the download, failure and completion states. A failed
// download keeps the old helper; the source is only replaced after a verified
// install and the rebind disables it for review.
func (m Model) showLarkUpdate() Model {
	u := m.larkUpdate
	if u == nil {
		return m
	}
	info := []pickInfoLine{
		{Label: "Helper", Value: u.current + " → " + u.release.Version},
		{Label: "Download", Value: u.release.ArchiveURL},
	}
	var rows []pickItem
	switch u.phase {
	case "installing":
		rows = append(rows, pickItem{Title: "Downloading, verifying and installing…", Heading: true})
		rows = append(rows, pickItem{Title: "Cancel", Value: extensionValue("back"), Info: info})
	case "failed":
		info = append(info,
			pickInfoLine{Label: "Status", Value: "Update failed; nothing was replaced."},
			pickInfoLine{Label: "Effect", Value: "Existing helper unchanged; Lark stays usable."},
			pickInfoLine{Label: "Error", Value: u.err},
		)
		rows = append(rows,
			pickItem{Title: "Retry", Value: extensionValue("lark-helper-update"), Info: info},
			pickItem{Title: "Cancel", Value: extensionValue("back"), Info: info},
		)
	case "done":
		info = append(info,
			pickInfoLine{Label: "Status", Value: fmt.Sprintf("Helper %s installed.", u.current)},
			pickInfoLine{Label: "Effect", Value: "Source disabled; review to enable the new bytes."},
		)
		rows = append(rows, pickItem{Title: "Back to Lark", Value: extensionValue("detail", "mcp", "lark"), Info: info})
	}
	if len(rows) == 0 {
		return m
	}
	m.menu.openPickerDetailed(rows, "", "extensions")
	m.menu.pickHint = "↑↓ Select · Enter · Esc Close"
	m.relayout()
	return m
}
