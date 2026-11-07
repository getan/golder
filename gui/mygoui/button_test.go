package mygoui

import (
	"image/color"
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
)

func buttonBorderPixel(t *testing.T, tt *ui.Tester, label string) (ui.Rect, color.RGBA) {
	t.Helper()
	box, ok := tt.Find(label)
	if !ok || box.W < 4 || box.H < 4 {
		t.Fatalf("button %q not found: %+v", label, box)
	}
	return box, tt.Image().RGBAAt(int(box.X+box.W/2), int(math.Ceil(float64(box.Y))))
}

func TestButtonBorderAppearsOnHoverWithoutLayoutShift(t *testing.T) {
	for _, dark := range []bool{false, true} {
		t.Run(map[bool]string{false: "light", true: "dark"}[dark], func(t *testing.T) {
			clicks := 0
			tt := ui.NewTester(func(c *ui.Context) {
				ui.Column(c).Padding(20).Gap(10).Children(func() {
					if subtleButton(c, "Normal").Label("normal button").Width(150).Clicked() {
						clicks++
					}
					subtlePrimaryButton(c, "Primary").Label("primary button").Width(150)
					subtleButton(c, "Fixed").Label("fixed-color button").Width(150).Background(ui.RGB(180, 40, 40))
					compactIconButton(c, "icon", browserMonochromeIcon, "icon button", "icon button")
					menu := ui.MenuButton(c, "Tabs", func(m *ui.Menu) { m.Item("One") }).Label("menu button")
					hoverBorder(c, menu)
					subtleButton(c, "Disabled", true).Label("disabled button").Width(150)
				})
			}, 260, 340)
			tt.SetDark(dark)
			for _, label := range []string{"normal button", "primary button", "fixed-color button", "icon button", "menu button"} {
				tt.Move(2, 2)
				beforeBox, before := buttonBorderPixel(t, tt, label)
				tt.Move(beforeBox.X+beforeBox.W/2, beforeBox.Y+beforeBox.H/2)
				afterBox, after := buttonBorderPixel(t, tt, label)
				if beforeBox != afterBox || before == after {
					t.Fatalf("%s hover: bounds %v -> %v, border %v -> %v", label, beforeBox, afterBox, before, after)
				}
				tt.Move(2, 2)
				_, away := buttonBorderPixel(t, tt, label)
				if away != before {
					t.Fatalf("%s border did not disappear after leaving: %v -> %v", label, before, away)
				}
			}
			tt.Move(2, 2)
			box, before := buttonBorderPixel(t, tt, "disabled button")
			tt.Move(box.X+box.W/2, box.Y+box.H/2)
			_, after := buttonBorderPixel(t, tt, "disabled button")
			if before != after {
				t.Fatalf("disabled button reacted to hover: %v -> %v", before, after)
			}
			if err := tt.Click("normal button"); err != nil || clicks != 1 {
				t.Fatalf("button click: clicks=%d err=%v", clicks, err)
			}
		})
	}
}

func TestButtonKeyboardFocusAndActivation(t *testing.T) {
	clicks := 0
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Padding(20).Children(func() {
			if subtleButton(c, "Action").Label("keyboard button").Clicked() {
				clicks++
			}
		})
	}, 200, 100)
	tt.Move(2, 2)
	box, before := buttonBorderPixel(t, tt, "keyboard button")
	tt.Key(0, ui.KeyTab)
	afterBox, after := buttonBorderPixel(t, tt, "keyboard button")
	if !tt.Focused("keyboard button") || box != afterBox || before == after {
		t.Fatalf("keyboard focus: focused=%t bounds %v -> %v, border %v -> %v", tt.Focused("keyboard button"), box, afterBox, before, after)
	}
	tt.Key(0, ui.KeyEnter)
	if clicks != 1 {
		t.Fatalf("keyboard activation: got %d clicks", clicks)
	}
}

func TestNewSessionPlusGetsHoverBorder(t *testing.T) {
	a := testApp()
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Move(2, 2)
	box, before := buttonBorderPixel(t, tt, "Create session in project")
	tt.Move(box.X+box.W/2, box.Y+box.H/2)
	afterBox, after := buttonBorderPixel(t, tt, "Create session in project")
	if box != afterBox || before == after {
		t.Fatalf("new-session plus hover: bounds %v -> %v, border %v -> %v", box, afterBox, before, after)
	}
}
