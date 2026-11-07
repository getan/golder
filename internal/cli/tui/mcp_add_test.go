package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/getan/golder/internal/extensions"
)

func TestMCPAddPasteKeepsCredentialsOutOfSession(t *testing.T) {
	m := extensionModel(t)
	next, _ := m.beginMCPAdd("json")
	m = next.(Model)
	raw := "{\n  \"mcpServers\": {\"newserver\": {\"url\": \"https://example.com/mcp\", \"headers\": {\"Authorization\": \"Bearer private-mcp-token\"}}}\n}"
	m = apply(t, m, tea.PasteMsg{Content: raw})
	if m.mcpWizard == nil || m.mcpWizard.phase != "review" {
		t.Fatalf("pasted JSON did not open review: %+v", m.mcpWizard)
	}
	secret := m.mcpWizard.drafts[0].Headers["Authorization"]
	if secret == "" || strings.Contains(m.View().Content, secret) || strings.Contains(m.input.Value(), secret) || strings.Contains(strings.Join(m.transcript.contentLines(), "\n"), secret) {
		t.Fatal("pasted MCP credential leaked into UI or conversation")
	}
	next, cmd := m.handleMCPWizardKey(runeKey('s'))
	m = next.(Model)
	if cmd == nil {
		t.Fatal("save did not schedule the operation")
	}
	m = apply(t, m, cmd())
	if m.mcpWizard.phase != "done" || m.mcpWizard.err != "" {
		t.Fatalf("save failed: %s", m.mcpWizard.err)
	}
	source, ok := m.opts.Extensions.Source(extensions.SourceRef{Kind: "mcp", ID: "newserver"})
	if !ok || source.Enabled || source.Trust == "approved" {
		t.Fatalf("save disabled changed authority: %+v", source)
	}
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.mcpWizard != nil || strings.Contains(m.View().Content, secret) {
		t.Fatal("closing wizard retained or displayed its secret")
	}
}

func TestMCPAddManualCommandDoesNotUseShell(t *testing.T) {
	m := extensionModel(t)
	next, _ := m.beginMCPAdd("stdio")
	m = next.(Model)
	m.mcpWizard.input.SetValue("local-new")
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m.mcpWizard.input.SetValue(`echo 'hello world' '; touch /tmp/not-run'`)
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	w := m.mcpWizard
	if w.phase != "review" || len(w.drafts) != 1 || len(w.drafts[0].Args) != 2 || w.drafts[0].Args[1] != "; touch /tmp/not-run" {
		t.Fatal("command was not parsed as fixed argv")
	}
	if w.drafts[0].Network {
		t.Fatal("local network access should default off")
	}
	m = apply(t, m, runeKey('n'))
	if !m.mcpWizard.drafts[0].Network {
		t.Fatal("network capability was not explicitly toggled")
	}
}

func TestMCPAddRejectsWhileRunning(t *testing.T) {
	m := extensionModel(t)
	m.running = true
	next, _ := m.openMCPAddPicker()
	if next.(Model).mcpWizard != nil || next.(Model).menu.active {
		t.Fatal("running agent must not open an MCP mutation workflow")
	}
}

func TestRemoteInputCannotStartDuringPrivateMCPReview(t *testing.T) {
	m := extensionModel(t)
	next, _ := m.beginMCPAdd("json")
	m = next.(Model)
	next, _ = m.Update(remoteInputMsg{text: "run a prompt"})
	m = next.(Model)
	if m.running || m.mcpWizard == nil {
		t.Fatal("remote input started a turn during private MCP review")
	}
	for _, block := range m.transcript.blocks {
		if strings.Contains(block.text, "run a prompt") {
			t.Fatal("remote prompt was admitted to the transcript")
		}
	}
}

func TestMCPManualBearerTokenStaysPrivate(t *testing.T) {
	m := extensionModel(t)
	next, _ := m.beginMCPAdd("http")
	m = next.(Model)
	m.mcpWizard.input.SetValue("manual")
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m.mcpWizard.input.SetValue("https://example.invalid/mcp")
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.mcpWizard.phase != "auth-token" {
		t.Fatal("manual HTTP source did not offer private token input")
	}
	secret := "fixture-" + "private-bearer"
	m.mcpWizard.input.SetValue(secret)
	if strings.Contains(m.View().Content, secret) {
		t.Fatal("Bearer token input was visible")
	}
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.mcpWizard.phase != "review" || m.mcpWizard.drafts[0].Headers["Authorization"] != "Bearer "+secret || strings.Contains(m.View().Content, secret) {
		t.Fatal("Bearer token was not retained privately for review")
	}
}

func TestMCPApprovalRequiresScrollingLongConnection(t *testing.T) {
	m := extensionModel(t)
	next, _ := m.beginMCPAdd("json")
	m = next.(Model)
	raw := `{"command":"echo","args":["` + strings.Repeat("x", 1800) + `"]}`
	m = apply(t, m, tea.PasteMsg{Content: raw})
	if m.mcpWizard == nil || m.mcpWizard.phase != "review" || m.mcpWizard.reviewMaxOffset(m.width, m.height) == 0 {
		t.Fatal("long connection was not rendered as a scrollable review")
	}
	next, cmd := m.handleMCPWizardKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	if cmd != nil || !strings.Contains(m.mcpWizard.err, "Scroll") {
		t.Fatal("approval was accepted before the review reached the end")
	}
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnd})
	next, cmd = m.handleMCPWizardKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil || next.(Model).mcpWizard.phase != "saving" {
		t.Fatal("reviewed connection could not be approved")
	}
}
