package mygoui

import (
	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
)

func (a *App) loadProxySettings() {
	a.proxyErr, a.proxyNote = "", ""
	settings, err := appsession.LoadProxySettings()
	if err != nil {
		a.proxyErr = err.Error()
		return
	}
	a.proxySettings = settings
}

func (a *App) saveProxySettings() {
	settings, err := appsession.SaveProxySettings(a.proxySettings)
	if err != nil {
		a.proxyErr, a.proxyNote = err.Error(), ""
		return
	}
	a.proxySettings, a.proxyErr = settings, ""
	a.proxyNote = "Saved. New GUI update and Lark/Meegle downloads use this proxy address. Provider settings apply to new requests; close and reopen existing browsers to apply their route."
}

func (a *App) proxySettingsView(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Padding(18, 20, 20, 20).Gap(14).Children(func() {
		ui.Text(c, "Proxy").FontSize(16).FontWeight(600)
		ui.Text(c, "One shared proxy address for downloads, Browser and Providers. Enter your proxy application's address and save it; no environment variable is required.").FontSize(12).TextColor(t.TextMuted).Wrap()
		ui.Field(c, "Proxy URL", func() {
			ui.TextInput(c, &a.proxySettings.URL).FillWidth().Label("Proxy address").Placeholder(appsession.DefaultProxyURL)
		})
		ui.Text(c, "Suggested local port: 7897 (Clash Verge). Change it if your proxy uses another port. HTTP, HTTPS and SOCKS5 are supported.").FontSize(11).TextColor(t.TextMuted).Wrap()
		a.downloadProxySettingsView(c)
		ui.Column(c).Padding(12).Gap(8).Radius(10).Background(t.Surface).Border(1, t.Border).Children(func() {
			ui.Text(c, "Browser").FontSize(14).FontWeight(600)
			ui.Checkbox(c, &a.proxySettings.Browser, "Use proxy for Browser")
			ui.Text(c, "Applies when a browser is next launched. Existing browsers retain their current route. Browser URLs cannot contain credentials or paths.").FontSize(11).TextColor(t.TextMuted).Wrap()
		})
		ui.Column(c).Padding(12).Gap(8).Radius(10).Background(t.Surface).Border(1, t.Border).Children(func() {
			ui.Text(c, "Providers").FontSize(14).FontWeight(600)
			ui.Text(c, "Select the providers whose requests use this proxy. Unselected providers connect directly.").FontSize(11).TextColor(t.TextMuted).Wrap()
			ui.Row(c).Gap(8).Children(func() {
				if subtleButton(c, "Enable all providers").Clicked() {
					for i := range a.proxySettings.Providers {
						a.proxySettings.Providers[i].Enabled = true
					}
				}
				if subtleButton(c, "Disable all providers").Clicked() {
					for i := range a.proxySettings.Providers {
						a.proxySettings.Providers[i].Enabled = false
					}
				}
			})
			ui.Row(c).Gap(8).Wrap().Children(func() {
				for i := range a.proxySettings.Providers {
					option := &a.proxySettings.Providers[i]
					ui.Column(c.Key("proxy-provider:" + option.Name)).Width(190).Children(func() {
						ui.Checkbox(c, &option.Enabled, option.Name)
					})
				}
			})
		})
	})
}

func (a *App) proxySettingsFooter(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Padding(8, 20, 12, 20).Gap(6).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
		if a.proxyErr != "" {
			ui.Text(c, a.proxyErr).FontSize(12).TextColor(t.Danger).Wrap()
		} else if a.proxyNote != "" {
			ui.Text(c, a.proxyNote).FontSize(12).TextColor(t.Success).Wrap()
		}
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if subtleButton(c, "Reload proxy settings").Clicked() {
				a.loadProxySettings()
			}
			if subtlePrimaryButton(c, "Save proxy settings").Clicked() {
				a.saveProxySettings()
			}
		})
	})
}
