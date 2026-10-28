package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestSetupDisplayIsTransientAndRejectsStaleEvents(t *testing.T) {
	m := extensionModel(t)
	m.extensionSetupEpoch = 2
	stream := make(chan tea.Msg, 1)
	private := "https://accounts.feishu.cn/device?user_code=operator-only"
	model, _ := m.Update(extensionSetupProgressMsg{epoch: 2, text: private, next: stream})
	m = model.(Model)
	transcriptView, _ := m.renderContent()
	if !strings.Contains(m.View().Content, private) || strings.Contains(transcriptView, private) {
		t.Fatal("setup content was not isolated to the transient operator display")
	}
	model, _ = m.Update(extensionSetupProgressMsg{epoch: 1, text: "stale", next: stream})
	m = model.(Model)
	if m.extensionSetupText != private {
		t.Fatal("stale setup event replaced the active authorization display")
	}
	model, _ = m.extensionDone(extensionDoneMsg{setupEpoch: 2})
	if model.(Model).extensionSetupText != "" {
		t.Fatal("setup display survived completion")
	}
}
