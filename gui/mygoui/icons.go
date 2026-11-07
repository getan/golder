package mygoui

import "github.com/egoist/mygo/ui"

const approvalHandSVG = `
<svg viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg">
  <path d="M7 11V5.5a1.5 1.5 0 0 1 3 0V9h1V3.5a1.5 1.5 0 0 1 3 0V9h1V4.5a1.5 1.5 0 0 1 3 0V9h1V6.5a1.5 1.5 0 0 1 3 0V14a8 8 0 0 1-8 8h-2.36a8 8 0 0 1-5.66-2.34L2.3 15.98a1.69 1.69 0 0 1 2.33-2.44L7 15.72V11Z" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/>
</svg>`

var approvalHandIcon = ui.MustParseSVG([]byte(approvalHandSVG))

var browserMonochromeIcon = ui.MustParseSVG([]byte(`
<svg viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg">
  <circle cx="12" cy="12" r="9" fill="none" stroke="currentColor" stroke-width="2"/>
  <path d="M3 12h18M12 3c3 3.4 3 14.6 0 18M12 3c-3 3.4-3 14.6 0 18" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"/>
</svg>`))

var terminalMonochromeIcon = ui.MustParseSVG([]byte(`
<svg viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg">
  <path d="M3 5h18v14H3zM7 9l3 3-3 3M13 15h4" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/>
</svg>`))

var computerMonochromeIcon = ui.MustParseSVG([]byte(`
<svg viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg">
  <path d="M4 4h16v12H4zM9 20h6M12 16v4" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/>
</svg>`))

var changesMonochromeIcon = ui.MustParseSVG([]byte(`
<svg viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg">
  <circle cx="6" cy="4" r="2" fill="none" stroke="currentColor" stroke-width="1.8"/>
  <circle cx="6" cy="20" r="2" fill="none" stroke="currentColor" stroke-width="1.8"/>
  <circle cx="18" cy="6" r="2" fill="none" stroke="currentColor" stroke-width="1.8"/>
  <path d="M6 6v12M18 8c0 4-3 6-6 6H6" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/>
</svg>`))

var filesMonochromeIcon = ui.MustParseSVG([]byte(`
<svg viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg">
  <path d="M6 3h8l4 4v14H6zM14 3v5h4M9 12h6M9 16h6" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/>
</svg>`))

var folderMonochromeIcon = ui.MustParseSVG([]byte(`
<svg viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg">
  <path d="M3 6h7l2 2h9v11H3z" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round"/>
</svg>`))

var hiddenFilesIcon = ui.MustParseSVG([]byte(`
<svg viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg">
  <path d="M2 12s3.5-6 10-6 10 6 10 6-3.5 6-10 6S2 12 2 12z" fill="none" stroke="currentColor" stroke-width="1.8"/>
  <circle cx="12" cy="12" r="3" fill="none" stroke="currentColor" stroke-width="1.8"/>
</svg>`))

var sidebarIcon = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24"><path d="M3 4h18v16H3zM9 4v16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round"/></svg>`))
var archiveIcon = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24"><path d="M4 8h16v12H4zM3 4h18v4H3zM9 12h6" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round"/></svg>`))
var settingsIcon = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="3" fill="none" stroke="currentColor" stroke-width="1.8"/><path d="M12 2v3M12 19v3M2 12h3M19 12h3M5 5l2 2M17 17l2 2M19 5l-2 2M7 17l-2 2" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"/></svg>`))
var updateIcon = ui.MustParseSVG([]byte(`<svg viewBox="0 0 24 24"><path d="M12 3v12M8 11l4 4 4-4M4 17v3h16v-3" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/></svg>`))

// Title actions have no default tile; the short line distinguishes a visible
// view from a requested but temporarily obscured one. Focus remains outlined.
func titleIconButton(c *ui.Context, key string, icon *ui.SVG, label, tooltip string, visible, present bool) ui.Element {
	t := c.Theme()
	indicator, color := ui.Transparent, t.Text
	if visible {
		indicator, color = t.Accent, t.Accent
	} else if present {
		indicator, color = t.TextMuted, t.TextMuted
		tooltip += " · hidden"
	}
	button := ui.ButtonBase(c.Key(key)).Size(30, 30).Radius(7).TextColor(color).Label(label).Tooltip(tooltip)
	hoverBorder(c, button)
	if button.Pressed() {
		button.Background(t.SurfacePressed)
	} else if button.Hovered() {
		button.Background(t.SurfaceHover)
	}
	button.Children(func() {
		ui.Column(c).Center().Gap(2).Children(func() {
			ui.Icon(c, icon).Size(16, 16)
			ui.Box(c).Size(12, 2).Background(indicator)
		})
	})
	return button
}

func compactIconButton(c *ui.Context, key string, icon *ui.SVG, label, tooltip string) ui.Element {
	t := c.Theme()
	button := ui.ButtonBase(c.Key(key)).Size(30, 30).Radius(7).
		Background(t.Surface).TextColor(t.Text).Label(label).Tooltip(tooltip)
	hoverBorder(c, button)
	switch {
	case button.Pressed():
		button.Background(t.SurfacePressed)
	case button.Hovered():
		button.Background(t.SurfaceHover)
	}
	button.Children(func() { ui.Icon(c, icon).Size(16, 16) })
	return button
}
