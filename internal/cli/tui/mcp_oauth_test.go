package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/getan/golder/internal/mcpauth"
)

func TestMCPAuthorizationReviewIsOperatorOnly(t *testing.T) {
	m := extensionModel(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := make(chan tea.Msg, 1)
	reply := make(chan bool, 1)
	m.mcpOAuth = &mcpOAuthState{epoch: 3, id: "remote", stage: "discovering", cancel: cancel, stream: stream}
	m.running = true
	msg := mcpOAuthReviewMsg{epoch: 3, review: mcpauth.Review{Host: "auth.example.com", Scopes: []string{"read"}}, reply: reply, stream: stream}
	next, _ := m.reviewMCPAuthorization(msg)
	m = next.(Model)
	if !strings.Contains(m.View().Content, "auth.example.com") || !strings.Contains(m.View().Content, "read") {
		t.Fatal("authorization review omitted host or scopes")
	}
	if strings.Contains(strings.Join(m.transcript.contentLines(), "\n"), "auth.example.com") {
		t.Fatal("authorization review leaked into the conversation")
	}
	m = apply(t, m, tea.PasteMsg{Content: "must-not-enter-composer"})
	if strings.Contains(m.input.Value(), "must-not-enter-composer") {
		t.Fatal("paste behind authorization entered the composer")
	}
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	select {
	case accepted := <-reply:
		if !accepted {
			t.Fatal("explicit operator consent was not forwarded")
		}
	default:
		t.Fatal("authorization opened without an explicit decision")
	}
	m = apply(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if ctx.Err() == nil {
		t.Fatal("escape did not cancel authorization")
	}
}
