package mygoui

import (
	"context"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
	"github.com/getan/golder/internal/remotecontrol"
)

func (a *App) startRemoteControl() {
	if a.remoteBusy || a.remoteServer != nil || a.ctrl == nil || a.hub == nil || a.win == nil {
		return
	}
	ctrl, win := a.ctrl, a.win
	adapter, err := appsession.NewMobileAdapter(ctrl)
	if err != nil {
		a.remoteErr = err.Error()
		return
	}
	a.remoteBusy, a.remoteErr = true, ""
	a.remoteStartSeq++
	seq := a.remoteStartSeq
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	a.remoteStartCancel = cancel
	go func() {
		defer cancel()
		server, pairURL, startErr := remotecontrol.StartSecure(ctx, adapter)
		delivered := make(chan struct{})
		win.Update(func() {
			defer close(delivered)
			if a.remoteStartSeq != seq {
				if server != nil {
					go server.Stop(context.Background())
				}
				return
			}
			a.remoteStartCancel = nil
			a.remoteBusy = false
			if ctx.Err() != nil {
				if server != nil {
					go server.Stop(context.Background())
				}
				a.remoteErr = "Remote control startup was cancelled or timed out."
				return
			}
			if startErr != nil {
				a.remoteErr = startErr.Error()
				return
			}
			if !a.remoteOpen || a.ctrl != ctrl || a.remoteServer != nil {
				go server.Stop(context.Background())
				return
			}
			release, err := a.hub.BeginPrivateAuthorization()
			if err != nil {
				a.remoteErr = err.Error()
				go server.Stop(context.Background())
				return
			}
			qr := authorizationQR(pairURL)
			if qr == nil {
				release()
				a.remoteErr = "Could not render the private pairing QR."
				go server.Stop(context.Background())
				return
			}
			a.remoteServer, a.remoteCtrl, a.remoteQR, a.remoteRelease = server, ctrl, qr, release
			go a.watchRemoteControl(server)
		})
		select {
		case <-delivered:
		case <-ctx.Done():
			if server != nil {
				_ = server.Stop(context.Background())
			}
		}
	}()
}

func (a *App) watchRemoteControl(server *remotecontrol.SecureServer) {
	win := a.win
	if win == nil {
		return
	}
	select {
	case request := <-server.PairRequests():
		win.Update(func() {
			if a.remoteServer == server && a.remoteRelease != nil {
				a.remotePair = &request
			}
		})
	case <-server.Done():
		win.Update(func() {
			if a.remoteServer == server {
				a.stopRemoteControl()
				a.remoteErr = "Remote control stopped or its Tailscale route changed."
			}
		})
		return
	}
	<-server.Done()
	win.Update(func() {
		if a.remoteServer == server {
			a.stopRemoteControl()
			a.remoteErr = "Remote control stopped or its Tailscale route changed."
		}
	})
}

func (a *App) confirmRemotePair(allow bool) {
	if a.remoteServer == nil || a.remotePair == nil || a.remoteRelease == nil {
		return
	}
	server, request, release := a.remoteServer, *a.remotePair, a.remoteRelease
	a.remoteQR, a.remotePair, a.remoteRelease = nil, nil, nil
	release()
	if err := server.ConfirmPair(request.ID, request.Code, allow); err != nil {
		a.remoteErr = err.Error()
		a.stopRemoteControl()
		return
	}
	if !allow {
		a.stopRemoteControl()
		a.remoteErr = "Pairing rejected."
		return
	}
	a.remoteErr = "Pairing confirmed. Finish connecting on your phone."
}

func (a *App) stopRemoteControl() {
	server, release := a.remoteServer, a.remoteRelease
	a.remoteStartSeq++
	if a.remoteStartCancel != nil {
		a.remoteStartCancel()
		a.remoteStartCancel = nil
	}
	a.remoteBusy = false
	a.remoteServer, a.remoteCtrl, a.remoteQR, a.remotePair, a.remoteRelease = nil, nil, nil, nil, nil
	if server != nil {
		server.Revoke()
	}
	if release != nil {
		release()
	}
	if server != nil {
		go server.Stop(context.Background())
	}
}

func (a *App) remoteControlModal(c *ui.Context) {
	t := c.Theme()
	ui.Modal(c, &a.remoteOpen, func() {
		ui.Column(c).Width(560).MaxHeight(700).Gap(12).Padding(18).Children(func() {
			ui.Text(c, "Remote control").FontSize(16).Bold()
			ui.Text(c, "Share this session with one paired phone over Tailscale HTTPS. The phone can read the conversation, send messages and answer supported approvals.").FontSize(12).TextColor(t.TextMuted).Wrap()
			if a.remoteErr != "" {
				ui.Text(c, a.remoteErr).FontSize(12).TextColor(t.Warning).Wrap()
			}
			if a.remoteBusy {
				ui.Text(c, "Checking the local Tailscale Serve configuration…").FontSize(12)
			} else if a.remoteServer == nil {
				ui.Text(c, "Configure a dedicated, Tailnet-only HTTPS Serve route to 127.0.0.1:18787 first. Golder will not change your Tailscale configuration.").FontSize(12).Wrap()
				if subtlePrimaryButton(c, "Start remote control").Clicked() {
					a.startRemoteControl()
				}
			} else {
				if a.remoteQR != nil {
					ui.Text(c, "Scan this private QR on your phone. Do not share it.").FontSize(12).Wrap()
					ui.Image(c, a.remoteQR).Size(240, 240)
				}
				if a.remotePair != nil {
					ui.Text(c, "Compare code "+a.remotePair.Code+" with "+a.remotePair.DeviceName).FontSize(13).Bold().Wrap()
					ui.Row(c).Gap(8).Children(func() {
						if subtleButton(c, "Reject").Clicked() {
							a.confirmRemotePair(false)
						}
						if subtlePrimaryButton(c, "Codes match — allow").Clicked() {
							a.confirmRemotePair(true)
						}
					})
				} else if a.remoteQR == nil {
					state := "Waiting for phone"
					if a.remoteServer.Connected() {
						state = "Phone connected"
					}
					ui.Text(c, state).FontSize(12)
				}
				if subtleButton(c, "Stop remote control").Clicked() {
					a.stopRemoteControl()
				}
			}
			if subtleButton(c, "Close").Clicked() {
				a.remoteOpen = false
			}
		})
	})
}
