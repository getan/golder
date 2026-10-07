// This file wires the remote-control bridge (internal/remotecontrol, #442) into
// the full-screen TUI, the counterpart to internal/cli/repl/remotecontrol.go.
// It adds the "/remote-control" command that starts/stops an in-process
// HTTP+WebSocket server mirroring the session to a paired browser on the LAN,
// mirrors transcript output to that browser, surfaces browser-submitted prompts
// as a tea.Msg, and routes side-effect tool-call confirmations to the browser
// while a client is connected (see trustApprovalGate).
//
// The non-remote path is unchanged: when no session is active the mirror is a
// no-op and waitRemoteInput returns a nil Cmd.
package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/getan/golder/internal/remotecontrol"
)

// remoteSession owns the running server + bridge for one /remote-control
// activation. It is stored on runSession (so buildConfig can reach it to install
// the confirm seam) and is nil until the command starts a session.
type remoteSession struct {
	server *remotecontrol.Server
	bridge *remotecontrol.Bridge
	url    string
}

// hasClient reports whether a browser is currently paired and connected.
func (rs *remoteSession) hasClient() bool {
	return rs != nil && rs.bridge != nil && rs.bridge.Enabled()
}

// sendOutput mirrors session text to the remote browser. It records into the
// server's replay ring even when no client is connected, so a browser that pairs
// mid-session is replayed the recent scrollback.
func (rs *remoteSession) sendOutput(text string) {
	if rs == nil || rs.server == nil || text == "" {
		return
	}
	rs.server.SendOutput(text)
}

// startRemote builds and starts the server+bridge, storing the session on the
// runSession. It returns the pairing URL, or an error if a server is already
// running or the listener could not bind.
func (s *runSession) startRemote() (string, error) {
	if s.remote != nil {
		return s.remote.url, fmt.Errorf("already running")
	}
	// Break the server↔bridge construction cycle: build the server (Sink), then
	// the bridge over it, then route client frames back to the bridge.
	// The disconnect callback closes over the bridge variable (assigned before
	// the server starts, and only invoked once a client has connected): when
	// the browser drops while a confirmation prompt is pending, the waiter is
	// denied fail-closed instead of parking the run until a manual interrupt.
	var bridge *remotecontrol.Bridge
	srv := remotecontrol.NewServer(remotecontrol.Config{
		OnClientDisconnect: func() {
			if bridge != nil {
				bridge.DenyAllPending()
			}
		},
	}, nil)
	bridge = remotecontrol.NewBridge(srv)
	srv.SetHandler(bridge)
	url, err := srv.Start()
	if err != nil {
		return "", err
	}
	s.remote = &remoteSession{server: srv, bridge: bridge, url: url}
	return url, nil
}

// stopRemote shuts down the running server and clears the session. It is a no-op
// when remote control is off.
func (s *runSession) stopRemote() {
	if s.remote == nil {
		return
	}
	_ = s.remote.server.Stop(context.Background())
	// Unblock the pending waitRemoteInput listener: without this its Cmd
	// goroutine would watch a channel that can never receive again.
	s.remote.bridge.Close()
	s.remote = nil
}

// runRemoteControl handles the /remote-control command and its stop/status
// subcommands. It mutates m.session.remote, folds a system block into the
// transcript, and returns the listener Cmd (waitRemoteInput) on a successful
// start so browser-submitted prompts begin arriving.
func (m Model) runRemoteControl(line string) (tea.Model, tea.Cmd) {
	m.transcript.addUser(line)
	m.input.Clear()
	m.menu.close()
	defer m.relayout()

	if m.session == nil {
		m.transcript.addSystem("(remote control unavailable: no active session)")
		return m, nil
	}
	arg := strings.TrimSpace(strings.TrimPrefix(line, "/remote-control"))
	switch arg {
	case "stop":
		if m.session.remote == nil {
			m.transcript.addSystem("remote control is not running")
			return m, nil
		}
		m.session.stopRemote()
		m.transcript.addSystem("remote control stopped")
		return m, nil
	case "status":
		if m.session.remote == nil {
			m.transcript.addSystem("remote control: off")
			return m, nil
		}
		state := "waiting for a browser to connect"
		if m.session.remote.hasClient() {
			state = "browser connected"
		}
		m.transcript.addSystem(fmt.Sprintf("remote control: on (%s)\n  %s", state, m.session.remote.url))
		return m, nil
	case "":
		if m.session.remote != nil {
			m.transcript.addSystem("remote control already running:\n  " + m.session.remote.url)
			return m, nil
		}
		url, err := m.session.startRemote()
		if err != nil {
			m.transcript.addSystem("remote control: " + err.Error())
			return m, nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Remote control started. Open this URL on a device on the same network:\n\n  %s\n", url)
		if qr, qerr := remotecontrol.Render(url); qerr == nil {
			b.WriteString("\n" + qr)
		}
		b.WriteString("\nRun /remote-control stop to end the session.")
		m.transcript.addSystem(b.String())
		return m, m.waitRemoteInput()
	default:
		m.transcript.addSystem("usage: /remote-control [stop|status]")
		return m, nil
	}
}

// remoteEcho mirrors visible transcript text to the paired browser. It is a
// no-op when remote control is off, so callers can invoke it unconditionally at
// each point the transcript gains content.
func (m Model) remoteEcho(text string) {
	if m.session != nil && m.session.remote != nil {
		m.session.remote.sendOutput(text)
	}
}

// waitRemoteInput returns a tea.Cmd that blocks on the bridge's remote-input
// channel and emits one remoteInputMsg per browser submission. The Update loop
// re-issues it after each so successive prompts keep arriving. It returns nil
// when remote control is off, which stops the listener.
func (m Model) waitRemoteInput() tea.Cmd {
	if m.session == nil || m.session.remote == nil || m.session.remote.bridge == nil {
		return nil
	}
	ch := m.session.remote.bridge.RemoteInput()
	return func() tea.Msg {
		text, ok := <-ch
		if !ok {
			return nil
		}
		return remoteInputMsg{text: text}
	}
}
