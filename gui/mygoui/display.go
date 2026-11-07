package mygoui

import (
	"fmt"
	"math"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
)

func (a *App) applyDisplayPreferences(c *ui.Context) {
	settings := a.display
	if settings.Theme == "" || settings.FontScale < .85 || settings.FontScale > 1.35 {
		settings = appsession.DefaultDisplaySettings()
	}
	c.SetZoom(settings.FontScale)
	switch settings.Theme {
	case "light":
		c.SetTheme(ui.LightTheme())
	case "dark":
		c.SetTheme(ui.DarkTheme())
	}
}

func (a *App) saveDisplaySettings() {
	a.displayErr, a.displayNote = "", ""
	if err := appsession.SaveDisplaySettings(a.display); err != nil {
		a.displayErr = err.Error()
		return
	}
	a.displayNote = "Display settings saved and applied."
}

func (a *App) displaySettingsView(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Padding(18, 20).Gap(16).Children(func() {
		ui.Text(c, "Display").FontSize(16).FontWeight(600)
		ui.Text(c, "Appearance is local to the native GUI. System mode follows the desktop light/dark preference; font size scales the complete interface consistently.").
			FontSize(12).TextColor(t.TextMuted).MaxWidth(650).Wrap()

		ui.Column(c).Gap(8).Children(func() {
			ui.Text(c, "Theme").FontSize(12).FontWeight(600)
			ui.Row(c).Gap(8).Children(func() {
				for _, option := range []struct{ value, label string }{{"system", "System"}, {"light", "Light"}, {"dark", "Dark"}} {
					option := option
					if subtleButton(c, option.label).Selected(a.display.Theme == option.value).Clicked() {
						a.display.Theme = option.value
						a.saveDisplaySettings()
					}
				}
			})
		})

		ui.Column(c).Gap(8).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Font size").FontSize(12).FontWeight(600)
				ui.Spacer(c)
				ui.Text(c, fmt.Sprintf("%d%%", int(math.Round(float64(a.display.FontScale*100))))).FontSize(11).TextColor(t.TextMuted)
			})
			scale := float64(a.display.FontScale * 100)
			slider := ui.Slider(c, &scale, 85, 135).FillWidth().Tooltip("Scale interface text and controls")
			if slider.Changed() {
				a.display.FontScale = float32(math.Round(scale)) / 100
				a.saveDisplaySettings()
			}
			ui.Row(c).Gap(8).Wrap().Children(func() {
				for _, option := range []struct {
					label string
					scale float32
				}{{"Compact", .92}, {"Default", 1.06}, {"Large", 1.15}, {"Extra large", 1.25}} {
					option := option
					selected := math.Abs(float64(a.display.FontScale-option.scale)) < .005
					if subtleButton(c, option.label).Selected(selected).Clicked() {
						a.display.FontScale = option.scale
						a.saveDisplaySettings()
					}
				}
			})
		})

		ui.Column(c).Padding(14).Gap(6).Radius(10).Background(t.Surface).Border(1, t.Border).Children(func() {
			ui.Text(c, "Preview").FontSize(15).FontWeight(600)
			ui.Text(c, "Tool output, project files and conversation text use the same interface scale.").FontSize(12).TextColor(t.TextMuted).Wrap()
			ui.Text(c, "func main() { /* native preview */ }").Font("monospace").FontSize(11.5).TextColor(t.Text)
		})

		if a.displayErr != "" {
			ui.Text(c, a.displayErr).FontSize(12).TextColor(t.Danger).Wrap()
		} else if a.displayNote != "" {
			ui.Text(c, a.displayNote).FontSize(12).TextColor(t.Success)
		}
	})
}
