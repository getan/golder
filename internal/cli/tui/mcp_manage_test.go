package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/getan/golder/internal/extensions"
)

func TestMCPEditIsReviewedAndSavedDisabled(t *testing.T) {
	m := extensionModel(t)
	ref := extensions.SourceRef{Kind: "mcp", ID: "remote"}
	if err := m.opts.Extensions.Approve(ref); err != nil {
		t.Fatal(err)
	}
	if err := m.opts.Extensions.Enable(ref); err != nil {
		t.Fatal(err)
	}
	next, _ := m.beginMCPEdit(ref.ID)
	m = next.(Model)
	if m.mcpWizard == nil || m.mcpWizard.edit == nil || m.mcpWizard.phase != "target" {
		t.Fatal("edit did not open a private connection editor")
	}
	m.mcpWizard.input.SetValue("https://new.example.com/mcp")
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.mcpWizard.phase != "review" || !strings.Contains(m.View().Content, "Previous approval will be revoked") {
		t.Fatal("edit did not show the trust change before saving")
	}
	next, cmd := m.handleMCPWizardKey(runeKey('s'))
	m = next.(Model)
	if cmd == nil {
		t.Fatal("edit did not schedule a save")
	}
	m = apply(t, m, cmd())
	if m.mcpWizard.phase != "done" || m.mcpWizard.err != "" {
		t.Fatalf("edit failed: %s", m.mcpWizard.err)
	}
	view, _ := m.opts.Extensions.Source(ref)
	if view.Enabled || view.Trust == "approved" {
		t.Fatal("editing kept the prior approval")
	}
}

func TestMCPRemoveNeedsConfirmation(t *testing.T) {
	m := extensionModel(t)
	next, cmd := m.confirmMCPRemove("remote")
	m = next.(Model)
	if cmd != nil || m.mcpRemoval == nil || len(m.menu.pick) != 2 || !m.opts.Extensions.HasSource(extensions.SourceRef{Kind: "mcp", ID: "remote"}) {
		t.Fatal("removal dialog changed the source before confirmation")
	}
	next, _ = m.chooseExtension(extensionValue("back"))
	if next.(Model).mcpRemoval != nil {
		t.Fatal("cancelling removal retained the confirmation")
	}
}

func TestMCPApprovalRequiresFullReviewAndDoesNotPersistSessionOnly(t *testing.T) {
	m := extensionModel(t)
	ref := extensions.SourceRef{Kind: "mcp", ID: "remote"}
	next, _ := m.beginMCPApproval(ref.ID)
	m = next.(Model)
	if m.mcpWizard == nil || !m.mcpWizard.approvalOnly || m.mcpWizard.reviewMaxOffset(m.width, m.height) == 0 {
		t.Fatal("expected scrollable approval review")
	}
	next, cmd := m.handleMCPWizardKey(runeKey('s'))
	m = next.(Model)
	if cmd != nil || !strings.Contains(m.mcpWizard.err, "Scroll") {
		t.Fatal("session-only approval did not require full review")
	}
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnd})
	next, cmd = m.handleMCPWizardKey(runeKey('s'))
	m = next.(Model)
	if cmd == nil {
		t.Fatal("reviewed session-only approval was not scheduled")
	}
	m = apply(t, m, cmd())
	view, _ := m.opts.Extensions.Source(ref)
	if !view.Enabled || view.Trust != "approved" || !strings.Contains(m.mcpWizard.note, "session") {
		t.Fatal("session-only approval did not enable the source")
	}
	edit, err := m.opts.Extensions.PrepareMCPEdit(ref.ID)
	if err != nil || edit.Config.Enabled {
		t.Fatal("session-only approval was persisted")
	}
}

func TestMCPEditAcceptsPrivateReplacementJSON(t *testing.T) {
	m := extensionModel(t)
	next, _ := m.beginMCPEdit("remote")
	m = next.(Model)
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = apply(t, m, runeKey('v'))
	if m.mcpWizard.phase != "edit-json" {
		t.Fatal("replacement JSON editor was not opened")
	}
	secret := "fixture-" + "replacement-token"
	raw := `{"url":"https://example.invalid/mcp","headers":{"Authorization":"` + secret + `"}}`
	m = apply(t, m, tea.PasteMsg{Content: raw})
	if m.mcpWizard.phase != "review" || m.mcpWizard.drafts[0].ID != "remote" || strings.Contains(m.View().Content, secret) {
		t.Fatal("replacement JSON was not kept private")
	}
	next, cmd := m.handleMCPWizardKey(runeKey('s'))
	m = next.(Model)
	if cmd == nil {
		t.Fatal("replacement was not saved")
	}
	m = apply(t, m, cmd())
	if m.mcpWizard.phase != "done" || m.mcpWizard.err != "" {
		t.Fatalf("replacement failed: %s", m.mcpWizard.err)
	}
}
