package tui

// Tests for the TUI's first-run trust dialog (codex parity): an undecided
// launch directory gets the picker before the first prompt, the answer is
// applied to the trust store and the session, and hooks — deferred while the
// directory was undecided — are wired exactly once.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/getan/golder/internal/trust"
)

// withEmptyTrustStore points GOLDER_HOME at a temp dir so trust.DefaultPath()
// resolves to an empty store (no decisions) instead of the user's real one.
func withEmptyTrustStore(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GOLDER_HOME", home)
	return home
}

func newTrustTestModel(t *testing.T) Model {
	t.Helper()
	store := newTestStore(t)
	s, _, err := newRunSessionWithStore(store, Options{})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}
	return NewModel(Options{}).withSession(s, nil)
}

func TestTrustPickerFirstRun(t *testing.T) {
	home := withEmptyTrustStore(t)
	m := newTrustTestModel(t)

	if !m.pendingTrust {
		t.Fatal("an undecided launch directory should mark the trust prompt pending")
	}
	if m.logoRunning {
		t.Error("the trust question must not run the logo entrance; it hides the dialog")
	}
	if m.session.hooksWired {
		t.Fatal("hooks must stay deferred while the trust question is open")
	}
	if !m.approval.active || m.approval.kind != "trust" {
		t.Fatalf("trust must be open before Init, active=%v kind=%q", m.approval.active, m.approval.kind)
	}
	if view := m.approvalView(100); !strings.Contains(view, "trust this folder") {
		t.Errorf("dialog should ask the trust question:\n%s", view)
	}

	// "Trust this folder (remembered)" is the s shortcut. It used to be p, which
	// the tool dialog uses for "this session": a key meaning "remember forever"
	// in one dialog and "just for now" in another is a key people get wrong, so
	// the ladder is now the same in both (y once, p session, s saved).
	got, _ := m.Update(tea.KeyPressMsg{Code: 's', Text: "s"})
	m = got.(Model)

	if !m.session.trusted {
		t.Error("choosing Trust should mark the session trusted")
	}
	if !m.session.hooksWired {
		t.Error("answering the trust question should wire the deferred hooks")
	}
	if m.approval.active {
		t.Error("the dialog should close after the answer")
	}
	mgr, err := trust.NewManager(trust.DefaultPath())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if res := mgr.NearestTrustDecision(m.session.cwd); !res.Found || res.Decision != trust.Trusted {
		t.Errorf("trust decision not saved: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(home, "trust.json")); err != nil {
		t.Errorf("trust.json should exist under GOLDER_HOME: %v", err)
	}
}

// Reproduced in the visible Linux tuios pane: Down reached Update at 7.7ms,
// trustPromptMsg at 8.4ms. The arrow was routed to the composer and the picker
// then opened on row zero. No wall-clock assertions needed: deliver keys before
// running any Init commands, exactly the ordering a real terminal can produce.
func TestTrustOwnsKeysBeforeInitCommands(t *testing.T) {
	withEmptyTrustStore(t)
	m := newTrustTestModel(t)
	initCmd := m.Init()
	if !strings.Contains(m.View().Content, "Do you trust this folder?") {
		t.Fatal("the first view must already contain the trust dialog")
	}
	m = apply(t, m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.input.Value() != "" {
		t.Fatal("early text must not leak into the composer behind trust")
	}
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.approval.selected != 1 {
		t.Fatal("the first Down must select session trust, before Init commands finish")
	}
	m = apply(t, m, tea.KeyPressMsg{Code: 'p', Text: "p"})
	if m.approval.active || !m.session.trusted {
		t.Fatal("an early session-trust answer must take effect")
	}
	// Commands already returned by Init must not later reopen/reset the dialog
	// after the person has moved its selection or answered it.
	if initCmd != nil {
		for _, cmd := range initCmd().(tea.BatchMsg) {
			if _, reopens := cmd().(trustPromptMsg); reopens {
				t.Fatal("Init must not asynchronously reopen an already answered trust question")
			}
		}
	}
}

func TestTrustPickerEscLeavesUntrusted(t *testing.T) {
	home := withEmptyTrustStore(t)
	m := newTrustTestModel(t)
	got, _ := m.Update(trustPromptMsg{})
	m = got.(Model)

	got, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = got.(Model)

	if m.session.trusted {
		t.Error("Esc must not grant trust")
	}
	if !m.session.hooksWired {
		t.Error("Esc should still wire the deferred hooks (untrusted)")
	}
	if m.menu.picking() {
		t.Error("the picker should close on Esc")
	}
	if _, err := os.Stat(filepath.Join(home, "trust.json")); !os.IsNotExist(err) {
		t.Error("Esc must not persist a trust decision")
	}
}

func TestTrustPromptSkippedWhenDecided(t *testing.T) {
	withEmptyTrustStore(t)
	// Save a decision for the launch directory before the session is built.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := trust.NewManager(trust.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.SetDecision(cwd, trust.Trusted); err != nil {
		t.Fatal(err)
	}

	m := newTrustTestModel(t)
	if m.pendingTrust {
		t.Error("a decided directory must not prompt")
	}
	if !m.logoRunning {
		t.Error("a decided directory should still animate a fresh session")
	}
	if !m.session.trusted {
		t.Error("a saved Trusted decision should mark the session trusted")
	}
	if !m.session.hooksWired {
		t.Error("a decided directory should wire hooks at construction")
	}
}

func TestTrustPromptSkippedWithApprove(t *testing.T) {
	withEmptyTrustStore(t)
	store := newTestStore(t)
	s, _, err := newRunSessionWithStore(store, Options{Approve: true})
	if err != nil {
		t.Fatalf("newRunSessionWithStore: %v", err)
	}
	m := NewModel(Options{Approve: true}).withSession(s, nil)
	if m.pendingTrust {
		t.Error("--approve must skip the trust prompt")
	}
	if !m.logoRunning {
		t.Error("--approve should still animate a fresh session")
	}
	if !m.session.trusted || !m.session.hooksWired {
		t.Error("--approve should grant session trust and wire hooks up front")
	}
}

// TestTrustPickerSettlesARunningLogo: a dialog that opens while the entrance
// is still armed (a resumed/resized model, or any future caller) must stop the
// animation rather than repainting over the question.
func TestTrustPickerSettlesARunningLogo(t *testing.T) {
	withEmptyTrustStore(t)
	m := newTrustTestModel(t)
	m.logoRunning = true
	got, _ := m.Update(trustPromptMsg{})
	m = got.(Model)
	if m.logoRunning {
		t.Error("opening the trust dialog must settle the logo")
	}
	if !m.approval.active || m.approval.kind != "trust" {
		t.Fatalf("trust prompt should be open, active=%v kind=%q", m.approval.active, m.approval.kind)
	}
}
