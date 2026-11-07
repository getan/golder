package mygoui

import "github.com/egoist/mygo/ui"

// hoverBorder reserves the border's space so hovering never shifts the layout.
// Keyboard focus stays visible even when the pointer is elsewhere.
func hoverBorder(c *ui.Context, button ui.Element) ui.Element {
	color := ui.Transparent
	if button.FocusVisible() {
		color = c.Theme().Focus
	} else if button.Hovered() || button.Pressed() {
		color = c.Theme().Border
	}
	return button.Border(1, color)
}

// The optional disabled state is applied before inspecting hover or focus.
func subtleButton(c *ui.Context, label string, disabled ...bool) ui.Element {
	button := ui.Button(c, label)
	if len(disabled) > 0 {
		button.Disabled(disabled[0])
	}
	return hoverBorder(c, button)
}

func subtlePrimaryButton(c *ui.Context, label string, disabled ...bool) ui.Element {
	button := ui.PrimaryButton(c, label)
	if len(disabled) > 0 {
		button.Disabled(disabled[0])
	}
	return hoverBorder(c, button)
}
