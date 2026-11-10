// This file owns the full-screen TUI's remote-control lifecycle. The interactive
// command starts the authenticated Tailnet v2 service through mobile_remote.go;
// the legacy LAN bridge remains only for its compatibility tests.
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
	server  *remotecontrol.Server
	bridge  *remotecontrol.Bridge
	url     string
	secure  *remotecontrol.SecureServer
	mobile  *mobileTUI
	closeQR func()
	pairing bool
	pair    *remotecontrol.PairRequest
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

// startRemote starts the legacy bridge on loopback for compatibility tests.
// The interactive command deliberately never calls this unencrypted path.
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
		Host: "127.0.0.1",
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
	if s.remote.secure != nil {
		s.remote.secure.Revoke()
	}
	if s.remote.closeQR != nil {
		s.remote.closeQR()
	}
	if s.remote.mobile != nil {
		s.remote.mobile.close()
	}
	if s.remote.secure != nil {
		go s.remote.secure.Stop(context.Background())
	}
	if s.remote.server != nil {
		_ = s.remote.server.Stop(context.Background())
	}
	// Unblock the pending waitRemoteInput listener: without this its Cmd
	// goroutine would watch a channel that can never receive again.
	if s.remote.bridge != nil {
		s.remote.bridge.Close()
	}
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
	if strings.HasPrefix(arg, "approve ") || arg == "deny" {
		rs := m.session.remote
		if rs == nil || rs.secure == nil || rs.pair == nil || !rs.pairing {
			m.transcript.addSystem("No remote pairing is awaiting confirmation.")
			return m, nil
		}
		allow := strings.HasPrefix(arg, "approve ")
		if allow && strings.TrimSpace(strings.TrimPrefix(arg, "approve ")) != rs.pair.Code {
			m.transcript.addSystem("Pairing code did not match; nothing was approved.")
			return m, nil
		}
		if err := rs.secure.ConfirmPair(rs.pair.ID, rs.pair.Code, allow); err != nil {
			m.transcript.addSystem("Pairing failed: " + err.Error())
			m.session.stopRemote()
			return m, nil
		}
		rs.pairing, rs.pair = false, nil
		if !allow {
			m.session.stopRemote()
			m.transcript.addSystem("Remote pairing rejected.")
		} else {
			m.transcript.addSystem("Remote pairing approved. Confirm on the phone to connect.")
		}
		return m, nil
	}
	switch arg {
	case "stop":
		if m.remoteStarting {
			if m.remoteStartCancel != nil {
				m.remoteStartCancel()
			}
			m.remoteStarting, m.remoteStartCancel = false, nil
			m.transcript.addSystem("Remote control startup cancelled.")
			return m, nil
		}
		if m.session.remote == nil {
			m.transcript.addSystem("remote control is not running")
			return m, nil
		}
		m.session.stopRemote()
		m.transcript.addSystem("remote control stopped")
		return m, nil
	case "status":
		if m.remoteStarting {
			m.transcript.addSystem("remote control: starting")
			return m, nil
		}
		if m.session.remote == nil {
			m.transcript.addSystem("remote control: off")
			return m, nil
		}
		state := "waiting for a browser to connect"
		if m.session.remote.hasClient() {
			state = "browser connected"
		}
		if m.session.remote.secure != nil && m.session.remote.secure.Connected() {
			state = "phone connected"
		}
		m.transcript.addSystem(fmt.Sprintf("remote control: on (%s)", state))
		return m, nil
	case "":
		if m.session.remote != nil {
			m.transcript.addSystem("remote control is already running")
			return m, nil
		}
		return m.startMobileRemote()
	default:
		m.transcript.addSystem(m.slash.UsageLine("remote-control"))
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
