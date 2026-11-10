package mygoui

import "github.com/egoist/mygo/ui"

func (a *App) downloadProxySettingsView(c *ui.Context) {
	t := c.Theme()
	s := a.proxySettings
	ui.Column(c).Padding(10).Gap(4).Radius(10).Background(t.Surface).Border(1, t.Border).Children(func() {
		ui.Text(c, "Downloads").FontSize(14).FontWeight(600)
		source := "Source: Saved in Golder Settings"
		if s.EnvOverride {
			source = "Source: Environment override (GOLDER_PROXY)"
		}
		switch s.EffectiveURL {
		case "":
			ui.Text(c, "Route: Default/environment network").FontSize(12)
			source = "Source: No Golder proxy configured"
		case "(invalid URL)":
			ui.Text(c, "Route: Blocked by invalid proxy settings").FontSize(12).TextColor(t.Warning)
		default:
			// EffectiveURL is a sanitized snapshot from the controller, never the
			// raw environment value or the not-yet-saved text field above.
			ui.Text(c, "Route: Proxy · "+s.EffectiveURL).FontSize(12).Wrap()
		}
		ui.Text(c, source).FontSize(10.5).TextColor(t.TextMuted).Wrap()
		ui.Text(c, "GUI updates and Lark/Meegle downloads use this route automatically, independently of the Browser and Provider switches. This shows configuration, not a connectivity test.").FontSize(10.5).TextColor(t.TextMuted).Wrap()
		if s.EnvOverride {
			ui.Text(c, "GOLDER_PROXY overrides the saved address. Remove or change it in the launching environment and restart Golder to use a different route.").FontSize(10.5).TextColor(t.Warning).Wrap()
		} else {
			ui.Text(c, "Address edits and the suggested default take effect only after Save proxy settings. The route above shows the last loaded/saved configuration.").FontSize(10.5).TextColor(t.TextMuted).Wrap()
		}
	})
}
