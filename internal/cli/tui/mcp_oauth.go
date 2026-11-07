package tui

import (
	"context"
	"errors"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/extensions"
	"github.com/getan/golder/internal/mcpauth"
)

// The authorization UI is operator-only: it never places URLs, login pages,
// authorization codes or tokens in the transcript or model context.
type mcpOAuthState struct {
	epoch  uint64
	id     string
	stage  string
	review mcpauth.Review
	reply  chan bool
	cancel context.CancelFunc
	stream <-chan tea.Msg
}

type mcpOAuthReviewMsg struct {
	epoch  uint64
	review mcpauth.Review
	reply  chan bool
	stream <-chan tea.Msg
}

type mcpOAuthProgressMsg struct {
	epoch  uint64
	stage  string
	stream <-chan tea.Msg
}

type mcpOAuthDoneMsg struct {
	epoch uint64
	err   error
}

var openMCPAuthorizationPage = agenttool.OpenAuthorizationPage

func waitMCPAuthorization(stream <-chan tea.Msg, epoch uint64) tea.Cmd {
	return func() tea.Msg {
		if msg, ok := <-stream; ok {
			return msg
		}
		return mcpOAuthDoneMsg{epoch: epoch, err: errors.New("authorization stopped")}
	}
}

func (m Model) startMCPAuthorization(id string) (tea.Model, tea.Cmd) {
	if m.running || m.opts.Extensions == nil {
		m.transcript.addSystem("Finish the current run before authorizing an MCP server.")
		m.menu.close()
		m.relayout()
		return m, nil
	}
	m.mcpOAuthEpoch++
	epoch := m.mcpOAuthEpoch
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	stream := make(chan tea.Msg, 8)
	m.mcpOAuth = &mcpOAuthState{epoch: epoch, id: id, stage: "discovering", cancel: cancel, stream: stream}
	m.running = true
	m.menu.close()
	manager, cwd := m.opts.Extensions, m.cwd
	return m, func() tea.Msg {
		go func() {
			defer close(stream)
			ref := extensions.SourceRef{Kind: "mcp", ID: id}
			if err := manager.Disconnect(ref); err != nil {
				stream <- mcpOAuthDoneMsg{epoch: epoch, err: err}
				return
			}
			open := func(ctx context.Context, review mcpauth.Review, startURL string) (func(), error) {
				reply := make(chan bool, 1)
				select {
				case stream <- mcpOAuthReviewMsg{epoch: epoch, review: review, reply: reply, stream: stream}:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				select {
				case ok := <-reply:
					if !ok {
						return nil, errors.New("operator declined authorization")
					}
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				select {
				case stream <- mcpOAuthProgressMsg{epoch: epoch, stage: "opening", stream: stream}:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				closePage, err := openMCPAuthorizationPage(ctx, cwd, startURL)
				if err != nil {
					return nil, err
				}
				select {
				case stream <- mcpOAuthProgressMsg{epoch: epoch, stage: "waiting", stream: stream}:
				case <-ctx.Done():
					closePage()
					return nil, ctx.Err()
				}
				return closePage, nil
			}
			err := manager.AuthorizeMCP(ctx, ref, open)
			stream <- mcpOAuthDoneMsg{epoch: epoch, err: err}
		}()
		return waitMCPAuthorization(stream, epoch)()
	}
}

func (m Model) reviewMCPAuthorization(msg mcpOAuthReviewMsg) (tea.Model, tea.Cmd) {
	if m.mcpOAuth == nil || m.mcpOAuth.epoch != msg.epoch {
		return m, nil
	}
	m.mcpOAuth.stage, m.mcpOAuth.review, m.mcpOAuth.reply = "review", msg.review, msg.reply
	return m, waitMCPAuthorization(msg.stream, msg.epoch)
}

func (m Model) progressMCPAuthorization(msg mcpOAuthProgressMsg) (tea.Model, tea.Cmd) {
	if m.mcpOAuth == nil || m.mcpOAuth.epoch != msg.epoch {
		return m, nil
	}
	m.mcpOAuth.stage = msg.stage
	return m, waitMCPAuthorization(msg.stream, msg.epoch)
}

func (m Model) handleMCPAuthorizationKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	s := m.mcpOAuth
	switch msg.String() {
	case "esc", "ctrl+c":
		s.stage = "cancelling"
		s.cancel()
	case "enter":
		if s.stage == "review" && s.reply != nil {
			s.reply <- true
			s.reply = nil
			s.stage = "opening"
		}
	}
	return m, nil
}

func (m Model) finishMCPAuthorization(msg mcpOAuthDoneMsg) (tea.Model, tea.Cmd) {
	if m.mcpOAuth == nil || m.mcpOAuth.epoch != msg.epoch {
		return m, nil
	}
	id := m.mcpOAuth.id
	m.mcpOAuth.cancel()
	m.mcpOAuth = nil
	m.running = false
	if msg.err != nil {
		m.transcript.addSystem("MCP authorization failed or was cancelled: " + extensions.DisplayText(msg.err.Error(), 1024))
	} else {
		m.transcript.addSystem("MCP authorization and connection completed for " + id + ".")
	}
	return m.openExtensions("mcp")
}

func (s *mcpOAuthState) view() string {
	var b strings.Builder
	b.WriteString("\n  MCP authorization · operator only\n\n")
	switch s.stage {
	case "review":
		b.WriteString("  Authorization server: " + extensions.DisplayText(s.review.Host, 256) + "\n")
		if len(s.review.Scopes) > 0 {
			b.WriteString("  Requested scopes: " + extensions.DisplayText(strings.Join(s.review.Scopes, ", "), 1024) + "\n")
		} else {
			b.WriteString("  Requested scopes: provider did not specify\n")
		}
		b.WriteString("\n  Enter opens a private Golder browser · Esc cancels")
	case "opening":
		b.WriteString("  Opening the private authorization browser…\n\n  Esc cancels")
	case "waiting":
		b.WriteString("  Complete login and consent in the private browser pane.\n  This page is not available to the agent.\n\n  Esc cancels")
	case "cancelling":
		b.WriteString("  Cancelling authorization and closing its browser…")
	default:
		b.WriteString("  Discovering the provider's authorization requirements…\n\n  Esc cancels")
	}
	return b.String()
}
