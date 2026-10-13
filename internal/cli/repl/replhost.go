// This file is the REPL's half of the multiplexer reporting (see
// internal/hostcompat): the moments the line-based driver knows about, wired
// onto the session it already assembles.
//
// It mirrors internal/cli/tui/sessionhost.go, deliberately: the two drivers
// report the same session state the same way, and every decision about what a
// state means lives in internal/hostcompat so they cannot drift apart. What is
// here is only *when* the REPL knows.
package repl

import (
	"io"

	"github.com/getan/golder/internal/hostcompat"
)

// hostStart (re)announces the session: which conversation this pane holds, how
// to reopen it, and that golder is at rest. It runs once the session id is
// known and again whenever the session under the pane changes (/resume,
// /fork, /clone, /import), because a host's idea of the conversation must
// follow the one golder actually runs.
func (d *replDeps) hostStart() {
	d.host.Start(d.hookDeps.SessionID, hostcompat.ResumeArgv(d.hookDeps.SessionID))
	d.hostMeta()
}

// hostTurnStart reports that golder took a prompt.
func (d *replDeps) hostTurnStart() {
	d.host.TurnStart()
	d.hostMeta()
}

// hostTurnEnd reports a finished turn, failed when the run stopped on an error
// rather than finishing its work. An interrupt is a person's own choice, not a
// failure, so it reports rest.
func (d *replDeps) hostTurnEnd(failed bool) {
	d.host.TurnEnd(failed)
}

// hostClose clears the pane's agent: golder is going away.
func (d *replDeps) hostClose() {
	d.host.Close()
}

// hostPrompt is the gates' OnPrompt hook: golder is about to ask the person
// something, and the pane is blocked until they answer.
func (d *replDeps) hostPrompt(question string) func() {
	return d.host.Block(hostcompat.KindApproval, question)
}

// hostMeta reports display metadata: the model the session runs and how full
// its context window is. Display only — the state comes from the lifecycle
// calls above.
func (d *replDeps) hostMeta() {
	if d.host == nil {
		return
	}
	kv := map[string]string{}
	if d.live != nil && d.live.Model != "" {
		kv[hostcompat.MetaModel] = d.live.Model
	}
	if d.telemetry != nil {
		for k, v := range hostcompat.ContextMeta(d.telemetry.LatestContextTokens(), d.telemetry.LatestContextWindow()) {
			kv[k] = v
		}
	}
	d.host.Meta(kv)
}

// hostErrLog is where a failed report is noted. A person whose rail stopped
// updating has no other way to find out why, so it is not discarded.
func hostErrLog(out io.Writer) io.Writer {
	if out == nil {
		return nil
	}
	return out
}

// hostReporter opens the reporter for the multiplexer around this pane, or nil
// outside one.
func hostReporter(errLog io.Writer) *hostcompat.Session {
	return hostcompat.OpenSession(hostcompat.Options{ErrLog: hostErrLog(errLog)})
}
