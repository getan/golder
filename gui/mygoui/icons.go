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

var computerMonochromeIcon = ui.MustParseSVG([]byte(`
<svg viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg">
  <path d="M4 4h16v12H4zM9 20h6M12 16v4" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/>
</svg>`))

var filesMonochromeIcon = ui.MustParseSVG([]byte(`
<svg viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg">
  <path d="M6 3h8l4 4v14H6zM14 3v5h4M9 12h6M9 16h6" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/>
</svg>`))

var folderMonochromeIcon = ui.MustParseSVG([]byte(`
<svg viewBox="0 0 24 24" xmlns="http://www.w3.org/2000/svg">
  <path d="M3 6h7l2 2h9v11H3z" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round"/>
</svg>`))

func compactIconButton(c *ui.Context, key string, icon *ui.SVG, label, tooltip string) ui.Element {
	t := c.Theme()
	button := ui.ButtonBase(c.Key(key)).Size(30, 30).Radius(7).Border(1, t.Border).
		Background(t.Surface).TextColor(t.Text).Label(label).Tooltip(tooltip)
	switch {
	case button.Pressed():
		button.Background(t.SurfacePressed)
	case button.Hovered():
		button.Background(t.SurfaceHover)
	}
	button.Children(func() { ui.Icon(c, icon).Size(16, 16) })
	return button
}
