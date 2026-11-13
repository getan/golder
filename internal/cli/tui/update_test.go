package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/getan/golder/internal/selfupdate"
)

func TestBackgroundUpdatePreservesInputRunApprovalAndPicker(t *testing.T) {
	for _, state := range []string{"idle", "typing", "running", "approval", "picker"} {
		t.Run(state, func(t *testing.T) {
			m := NewModel(Options{Version: "1.5.93"})
			m = apply(t, m, tea.WindowSizeMsg{Width: 90, Height: 30})
			if state == "typing" {
				m.input.SetValue("unfinished draft")
			}
			m.running = state == "running"
			m.approval.active = state == "approval"
			if state == "picker" {
				m.menu.openPicker([]string{"keep-this"}, "")
			}
			draft, kind, active := m.input.Value(), m.menu.pickKind, m.menu.active
			blocks := len(m.transcript.blocks)
			m.updates.checking = true
			m = apply(t, m, updateCheckedMsg{latest: "v1.5.94", offer: true})
			if m.input.Value() != draft || m.menu.active != active || m.menu.pickKind != kind || len(m.transcript.blocks) != blocks || m.running != (state == "running") || m.approval.active != (state == "approval") {
				t.Fatal("background check interrupted an interaction or added transcript noise")
			}
			if !strings.Contains(m.updateHint(90), "/update") || m.updates.checking {
				t.Fatal("result did not set the nonblocking hint")
			}
			m.updates.checking = true
			m = apply(t, m, updateCheckedMsg{err: errors.New("offline")})
			if m.updates.latest != "v1.5.94" || len(m.transcript.blocks) != blocks {
				t.Fatal("background failure cleared a known release or spammed the transcript")
			}
		})
	}
}

func TestUpdatePickerIsExplicitAndSupportsSessionDismissal(t *testing.T) {
	m := NewModel(Options{Version: "1.5.93"})
	m.updates.latest, m.updates.offer = "v1.5.94", true
	m = typeCommand(t, m, "/update")
	if !m.menu.active || m.menu.pickKind != "update" || len(m.menu.pick) != 3 {
		t.Fatal("explicit /update did not open the choices")
	}
	if !strings.Contains(m.menu.pick[0].Title, "instructions") || m.quitting {
		t.Fatal("update choice must not install or quit")
	}
	// Drive the same selection path as keyboard Enter.
	m.menu.selected = 1
	next, _ := m.submitSlashSelected()
	m = next.(Model)
	if m.menu.active || m.updateHint(90) != "" || m.quitting {
		t.Fatal("session dismissal did not silence the reminder")
	}
	m.updates.checking = true
	m = apply(t, m, updateCheckedMsg{latest: "v1.5.94", offer: true})
	if m.updateHint(90) != "" {
		t.Fatal("hourly recheck re-offered a muted version")
	}
	m.updates.checking = true
	m = apply(t, m, updateCheckedMsg{latest: "v1.5.95", offer: true})
	if !strings.Contains(m.updateHint(90), "v1.5.95") {
		t.Fatal("a newer release should be offered after session dismissal")
	}
}

func TestSkipUpdateUsesStartupPreferenceAndSurvivesInflightResult(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	m := NewModel(Options{Version: "1.5.93"})
	m.updates.latest, m.updates.offer = "v1.5.94", true
	m = typeCommand(t, m, "/update skip")
	if m.updateHint(90) != "" || selfupdate.StartupOffer("1.5.93", "v1.5.94") {
		t.Fatal("skip did not persist to the shared startup preference")
	}
	m.updates.checking = true
	m = apply(t, m, updateCheckedMsg{latest: "v1.5.94", offer: true})
	if m.updateHint(90) != "" {
		t.Fatal("a stale result undid the skip")
	}
	if !selfupdate.StartupOffer("1.5.93", "v1.5.95") {
		t.Fatal("skip should not hide a newer release")
	}
}

func TestManualUpdateResultDoesNotStealFocusAfterTyping(t *testing.T) {
	m := NewModel(Options{Version: "1.5.93"})
	if m.beginUpdateCheck(true) == nil {
		t.Fatal("manual check did not start")
	}
	if m.beginUpdateCheck(false) != nil {
		t.Fatal("duplicate background check was launched")
	}
	m.input.SetValue("new draft")
	m = apply(t, m, updateCheckedMsg{latest: "v1.5.94", offer: true})
	if m.menu.active || m.input.Value() != "new draft" || m.updateHint(90) == "" {
		t.Fatal("delayed manual result stole focus")
	}
}

func TestUpdateMonitorCancellationAndSourceBuild(t *testing.T) {
	m := NewModel(Options{Version: "1.5.93"})
	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan struct{}, 1)
	m.updates.ctx, m.updates.ticks = ctx, ticks
	cmd := m.waitUpdateTick()
	cancel()
	ticks <- struct{}{}
	if cmd() != nil || m.beginUpdateCheck(false) != nil {
		t.Fatal("cancelled monitor still checked for updates")
	}
	m = NewModel(Options{Version: "dev"})
	stop := m.startUpdateMonitor()
	defer stop()
	if m.waitUpdateTick() != nil || m.beginUpdateCheck(false) != nil {
		t.Fatal("source build started background update checks")
	}
}

func TestUpdateInstructionsDoNotInstallOrQuit(t *testing.T) {
	m := NewModel(Options{Version: "1.5.93"})
	m.updates.latest = "v1.5.94"
	m = typeCommand(t, m, "/update info")
	text := strings.Join(blockTexts(m.transcript), "\n")
	for _, want := range []string{"golder update", "brew upgrade golder", "Nothing was installed", "/releases/tag/v1.5.94"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing update guidance %q", want)
		}
	}
	if m.running || m.quitting {
		t.Fatal("update guidance changed the session lifecycle")
	}
}

func TestTUIUpdateOptOutOnlyDisablesAutomaticChecks(t *testing.T) {
	for _, disabled := range []string{"flag", "environment"} {
		t.Run(disabled, func(t *testing.T) {
			t.Setenv("GOLDER_NO_UPDATE_CHECK", "0")
			m := NewModel(Options{Version: "1.5.93", NoUpdateCheck: disabled == "flag"})
			if disabled == "environment" {
				t.Setenv("GOLDER_NO_UPDATE_CHECK", "1")
			}
			stop := m.startUpdateMonitor()
			defer stop()
			if m.waitUpdateTick() != nil {
				t.Fatal("automatic monitoring ignored opt-out")
			}
			if m.beginUpdateCheck(true) == nil {
				t.Fatal("explicit /update must remain available")
			}
		})
	}
}

func TestUpdateHintFitsComposerAndManualCheckOpensPickerWhenIdle(t *testing.T) {
	m := NewModel(Options{Version: "1.5.93"})
	m = apply(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.updates.checking, m.updates.openAfter = true, true
	m = apply(t, m, updateCheckedMsg{latest: "v1.5.94", offer: true})
	if !m.menu.picking() || m.menu.pickKind != "update" {
		t.Fatal("idle explicit check should open the picker")
	}
	m.menu.close()
	m.relayout()
	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "/update") || strings.Count(view, "\n")+1 > 24 {
		t.Fatalf("update hint broke fixed-height layout: %q", view)
	}
}
