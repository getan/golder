package mygoui

import (
	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
)

func (a *App) loadSearchSettings() {
	a.searchErr, a.searchNote = "", ""
	settings, err := appsession.LoadSearchSettings()
	if err != nil {
		a.searchErr = err.Error()
		return
	}
	a.searchSettings = settings
}

func (a *App) saveSearchSettings() {
	settings, err := appsession.SaveSearchSettings(a.searchSettings)
	if err != nil {
		a.searchErr, a.searchNote = err.Error(), ""
		return
	}
	a.searchSettings, a.searchErr = settings, ""
	a.searchNote = "Saved. New web searches use these providers immediately."
}

func (a *App) searchSettingsView(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Padding(18, 20, 20, 20).Gap(14).Children(func() {
		ui.Text(c, "Web search").FontSize(16).FontWeight(600)
		ui.Text(c, "Providers are tried in this order: Tavily → Exa → DeepSeek → DuckDuckGo. Configure one key; the first configured provider wins. With no keys, the keyless DuckDuckGo fallback is used.").
			FontSize(12).TextColor(t.TextMuted).Wrap()
		ui.Field(c, "Tavily API key", func() {
			ui.TextInput(c, &a.searchSettings.Tavily).Password().FillWidth().Label("Tavily API key").Placeholder("tvly-…")
		})
		ui.Text(c, "Get a key: https://tavily.com (free tier).").FontSize(11).TextColor(t.TextMuted).Wrap()
		ui.Field(c, "Exa API key", func() {
			ui.TextInput(c, &a.searchSettings.Exa).Password().FillWidth().Label("Exa API key").Placeholder("exa-…")
		})
		ui.Text(c, "Get a key: https://exa.ai.").FontSize(11).TextColor(t.TextMuted).Wrap()
		ui.Field(c, "DeepSeek API key", func() {
			ui.TextInput(c, &a.searchSettings.DeepSeek).Password().FillWidth().Label("DeepSeek API key").Placeholder("sk-…")
		})
		ui.Text(c, "Get a key: https://platform.deepseek.com/api_keys. This is the same key as the DeepSeek model API.").FontSize(11).TextColor(t.TextMuted).Wrap()
		ui.Text(c, "Keys are stored in the encrypted named-credential file, not config.toml. The order is fixed; configuring one key is enough.").
			FontSize(11).TextColor(t.TextMuted).Wrap()
	})
}

func (a *App) searchSettingsFooter(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Padding(8, 20, 12, 20).Gap(6).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
		if a.searchErr != "" {
			ui.Text(c, a.searchErr).FontSize(12).TextColor(t.Danger).Wrap()
		} else if a.searchNote != "" {
			ui.Text(c, a.searchNote).FontSize(12).TextColor(t.Success).Wrap()
		}
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if subtleButton(c, "Reload search settings").Clicked() {
				a.loadSearchSettings()
			}
			if subtlePrimaryButton(c, "Save search settings").Clicked() {
				a.saveSearchSettings()
			}
		})
	})
}
