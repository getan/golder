// This file is the TUI's half of the multiplexer reporting (see
// internal/hostcompat): the four moments the session knows about, plus the one
// the gates know about, wired onto the session the TUI already assembles.
//
// It is deliberately thin. Every decision — what a state means, how it reaches
// tuios or herdr, what happens when a prompt and a turn overlap — lives in
// internal/hostcompat, so the REPL, headless and the TUI cannot drift apart on
// what the person's rail shows. What is here is only *when* the TUI knows.
package tui

import (
	"io"
	"os"

	"github.com/getan/golder/internal/hostcompat"
)

// hostLogWriter is where a failed report is noted. Nil would drop them
// silently, which is the wrong default for an interactive session: a person
// whose rail stopped updating has no other way to find out why.
func hostLogWriter(_ Options) io.Writer { return os.Stderr }

// hostStart (re)announces the session: which conversation this pane holds, how
// to reopen it, and that golder is at rest. It runs at assembly and again
// whenever the session under the pane changes (/resume, /clone, /fork, or a
// rebuilt one), because a host's idea of the conversation must follow the one
// golder is actually running.
func (s *runSession) hostStart() {
	if s == nil {
		return
	}
	s.host.Start(s.hookDeps.SessionID, hostcompat.ResumeArgv(s.hookDeps.SessionID))
	s.hostMeta()
}

// hostTurnStart reports that golder took a prompt.
func (s *runSession) hostTurnStart() {
	if s == nil {
		return
	}
	s.host.TurnStart()
	s.hostMeta()
}

// hostTurnEnd reports a finished turn, failed when the run stopped on an error
// rather than finishing its work.
func (s *runSession) hostTurnEnd(failed bool) {
	if s == nil {
		return
	}
	s.host.TurnEnd(failed)
}

// hostClose clears the pane's agent: golder is going away. Without it the host
// keeps drawing a golder that has quit until it notices the shell prompt.
func (s *runSession) hostClose() {
	if s == nil {
		return
	}
	s.host.Close()
}

// hostPrompt is the gates' OnPrompt hook: golder is about to ask the person
// something, and the pane is blocked until they answer. The returned function
// is the answer, however it went.
//
// The kind is decided from the question's own words, which is what the gates
// put there: a read-scope request says what may be read, everything else is an
// approval of a tool call.
func (s *runSession) hostPrompt(question string) func() {
	if s == nil {
		return func() {}
	}
	return s.host.Block(hostcompat.KindApproval, question)
}

// hostMeta reports display metadata: the model the session runs and how full
// its context window is. It is display only — the state comes from the
// lifecycle calls above.
func (s *runSession) hostMeta() {
	if s == nil || s.host == nil {
		return
	}
	kv := map[string]string{}
	if s.live != nil && s.live.Model != "" {
		kv[hostcompat.MetaModel] = s.live.Model
	}
	for k, v := range hostcompat.ContextMeta(s.hostContextTokens(), s.hostContextWindow()) {
		kv[k] = v
	}
	s.host.Meta(kv)
}

// hostContextTokens is the most recent context reading the session has, from
// its telemetry holder (the same figure the status bar shows).
func (s *runSession) hostContextTokens() int {
	if s.telemetry == nil {
		return 0
	}
	return s.telemetry.LatestContextTokens()
}

// hostContextWindow is the window size that reading is against.
func (s *runSession) hostContextWindow() int {
	if s.telemetry == nil {
		return 0
	}
	return s.telemetry.LatestContextWindow()
}
