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
	if m.session.hooksWired {
		t.Fatal("hooks must stay deferred while the trust question is open")
	}

	got, _ := m.Update(trustPromptMsg{})
	m = got.(Model)
	if !m.approval.active || m.approval.kind != "trust" {
		t.Fatalf("trust prompt should open the approval dialog, active=%v kind=%q", m.approval.active, m.approval.kind)
	}
	if view := m.approvalView(100); !strings.Contains(view, "trust this folder") {
		t.Errorf("dialog should ask the trust question:\n%s", view)
	}

	// "Trust this folder" is the p shortcut (remember the decision).
	got, _ = m.Update(tea.KeyPressMsg{Code: 'p', Text: "p"})
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
	if !m.session.trusted || !m.session.hooksWired {
		t.Error("--approve should grant session trust and wire hooks up front")
	}
}
