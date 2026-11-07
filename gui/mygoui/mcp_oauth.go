package mygoui

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/getan/golder/internal/extensions"
	"github.com/getan/golder/internal/mcpauth"
)

// guiMCPAuthorization holds operator-only UI state. The web view has an
// ephemeral data store and no model-facing browser endpoint.
type guiMCPAuthorization struct {
	id     string
	stage  string
	review mcpauth.Review
	reply  chan bool
	view   *mygo.WebView
	cancel context.CancelFunc
	err    string
	host   string
}

func (a *App) startMCPAuthorization(id string) {
	if a.extensionBusy || a.mcpOAuth != nil || a.hub == nil || a.win == nil {
		return
	}
	release, err := a.hub.BeginPrivateAuthorization()
	if err != nil {
		a.extensionErr = err.Error()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	s := &guiMCPAuthorization{id: id, stage: "discovering", cancel: cancel}
	a.mcpOAuth, a.extensionBusy, a.extensionErr = s, true, ""
	win, ctrl := a.win, a.ctrl
	go func() {
		defer cancel()
		defer release()
		open := func(ctx context.Context, review mcpauth.Review, startURL string) (func(), error) {
			reply := make(chan bool, 1)
			win.Update(func() {
				if a.mcpOAuth == s && ctx.Err() == nil {
					s.stage, s.review, s.reply = "review", review, reply
				}
			})
			select {
			case allowed := <-reply:
				if !allowed {
					return nil, errors.New("authorization cancelled")
				}
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			view, err := win.NewWebView(mygo.WebViewOptions{
				URL:  "about:blank",
				Page: mygo.PageOptions{Untrusted: true, Session: mygo.NewEphemeralSession()},
			})
			if err != nil {
				return nil, errors.New("the built-in authorization browser is unavailable")
			}
			if ctx.Err() != nil {
				view.Destroy()
				return nil, ctx.Err()
			}
			view.Page().OnWillNavigate(func(event *mygo.NavigateEvent) {
				if !privateAuthorizationNavigation(event.URL, startURL, review.Host) {
					event.PreventDefault()
				}
			})
			view.Page().OnDidNavigate(func(raw string) {
				u, err := url.Parse(raw)
				if err == nil && u.Host != "" {
					win.Update(func() {
						if a.mcpOAuth == s {
							s.host = u.Host
						}
					})
				}
			})
			if err := view.Page().LoadURL(startURL); err != nil {
				view.Destroy()
				return nil, errors.New("the built-in authorization browser could not open the login page")
			}
			win.Update(func() {
				if a.mcpOAuth == s && ctx.Err() == nil {
					s.stage, s.view = "browser", view
				}
			})
			return func() {
				view.Destroy()
				win.Update(func() {
					if a.mcpOAuth == s {
						s.view, s.stage = nil, "finishing"
					}
				})
			}, nil
		}
		err := ctrl.AuthorizeMCP(ctx, id, open)
		win.Update(func() {
			if a.mcpOAuth != s {
				return
			}
			s.cancel, s.reply, s.view = nil, nil, nil
			a.extensionBusy = false
			if err != nil {
				s.stage, s.err = "failed", extensions.DisplayText(err.Error(), 1024)
			} else {
				s.stage = "done"
			}
			a.loadExtensionSettings()
		})
	}()
}

// Only HTTPS and the exact local callback/start listener are permitted. The
// reviewed loopback provider is allowed for local MCP development.
func privateAuthorizationNavigation(raw, startURL, reviewedHost string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Hostname() == "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	if u.Scheme != "http" {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	if !strings.EqualFold(u.Hostname(), "localhost") && (ip == nil || !ip.IsLoopback()) {
		return false
	}
	start, err := url.Parse(startURL)
	return err == nil && (strings.EqualFold(u.Host, start.Host) || strings.EqualFold(u.Host, reviewedHost))
}

func (a *App) mcpAuthorizationView(c *ui.Context) {
	s := a.mcpOAuth
	if s == nil {
		return
	}
	t := c.Theme()
	ui.Column(c).Padding(14).Gap(10).Radius(12).Background(t.Surface).Border(1, t.Border).Children(func() {
		ui.Text(c, "Private MCP authorization · "+s.id).FontSize(15).FontWeight(600)
		switch s.stage {
		case "review":
			ui.Text(c, "Authorization server: "+extensions.DisplayText(s.review.Host, 256)).FontSize(12).Wrap()
			scopes := strings.Join(s.review.Scopes, ", ")
			if scopes == "" {
				scopes = "not specified by provider"
			}
			ui.Text(c, "Requested scopes: "+extensions.DisplayText(scopes, 1024)).FontSize(12).Wrap()
			if subtlePrimaryButton(c, "Open private browser").Clicked() && s.reply != nil {
				s.reply <- true
				s.reply, s.stage = nil, "opening"
			}
		case "browser":
			ui.Text(c, "Complete login and consent here. The agent cannot access this page.").FontSize(11).TextColor(t.TextMuted).Wrap()
			if s.host != "" {
				ui.Text(c, "Current site: "+extensions.DisplayText(s.host, 256)).FontSize(11).TextColor(t.TextMuted).Wrap()
			}
			if s.view != nil {
				ui.WebView(c, s.view).FillWidth().Height(380)
			}
		case "done":
			ui.Text(c, "MCP connection completed.").FontSize(12).TextColor(t.Success)
		case "failed":
			ui.Text(c, extensions.DisplayText(s.err, 1024)).FontSize(12).TextColor(t.Danger).Wrap()
		default:
			ui.Text(c, "Preparing authorization…").FontSize(12).TextColor(t.TextMuted)
		}
		if s.stage == "done" || s.stage == "failed" {
			if subtleButton(c, "Close").Clicked() {
				a.mcpOAuth = nil
			}
		} else if subtleButton(c, "Cancel authorization").Clicked() && s.cancel != nil {
			s.stage = "cancelling"
			s.cancel()
		}
	})
}
