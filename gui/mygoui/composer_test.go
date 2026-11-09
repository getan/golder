package mygoui

import (
	"math"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
)

func TestSendAndStopButtonsUseNeutralPalette(t *testing.T) {
	theme := ui.LightTheme()
	face, _, _, text := composerControlPalette(theme, false, true)
	if face != theme.SurfacePressed || text != theme.Text || face == theme.Accent || face == theme.Danger {
		t.Fatalf("ready send palette face=%v text=%v", face, text)
	}
	face, _, _, text = composerControlPalette(theme, true, true)
	if face != theme.SurfacePressed || text != theme.Text || face == theme.Danger {
		t.Fatalf("stop palette face=%v text=%v", face, text)
	}
	face, _, _, text = composerControlPalette(theme, false, false)
	if face != theme.Surface || text != theme.TextMuted {
		t.Fatalf("disabled send palette face=%v text=%v", face, text)
	}
}

func TestComposerShowsReadOnlySessionOwner(t *testing.T) {
	a := testApp()
	a.status.ReadOnly = true
	a.status.Writer = appsession.SessionWriterView{Frontend: "TUI", PID: 4242}
	tt := ui.NewTester(a.view, 1000, 700)
	for _, want := range []string{"Read-only · active in TUI (PID 4242)", "Send message"} {
		if !tt.HasText(want) {
			t.Fatalf("read-only composer missing %q; texts=%q", want, tt.Texts())
		}
	}
}

func TestComposerButtonBordersAppearOnlyOnHover(t *testing.T) {
	for _, dark := range []bool{false, true} {
		t.Run(map[bool]string{false: "light", true: "dark"}[dark], func(t *testing.T) {
			a := testApp()
			a.draft = "ready"
			a.attachments = []appsession.UserAttachment{{Path: "/tmp/example.txt"}}
			tt := ui.NewTester(a.composer, 900, 300)
			tt.SetDark(dark)
			for _, label := range []string{"+", "Approval mode", "test-provider · test-model · off", "Send message", "×"} {
				tt.Move(2, 2)
				box, ok := tt.Find(label)
				if !ok {
					t.Fatalf("button %q not found: %q", label, tt.Texts())
				}
				x, y := int(box.X+box.W/2), int(math.Ceil(float64(box.Y)))
				before := tt.Image().RGBAAt(x, y)
				tt.Move(box.X+box.W/2, box.Y+box.H/2)
				afterBox, ok := tt.Find(label)
				after := tt.Image().RGBAAt(x, y)
				if !ok || box != afterBox || before == after {
					t.Fatalf("%s hover: bounds %v -> %v, border %v -> %v", label, box, afterBox, before, after)
				}
				tt.Move(2, 2)
				if away := tt.Image().RGBAAt(x, y); away != before {
					t.Fatalf("%s border remained after hover: %v -> %v", label, before, away)
				}
			}
		})
	}
}

func TestComposerStopButtonBorderAppearsOnHover(t *testing.T) {
	a := testApp()
	a.status.Running = true
	tt := ui.NewTester(a.composer, 900, 300)
	tt.Move(2, 2)
	box, ok := tt.Find("Stop generation")
	if !ok {
		t.Fatalf("stop button missing: %q", tt.Texts())
	}
	x, y := int(box.X+box.W/2), int(math.Ceil(float64(box.Y)))
	before := tt.Image().RGBAAt(x, y)
	tt.Move(box.X+box.W/2, box.Y+box.H/2)
	if after := tt.Image().RGBAAt(x, y); after == before {
		t.Fatalf("stop button hover did not show its border: %v", after)
	}
}

func TestDisabledComposerButtonsDoNotReactToHover(t *testing.T) {
	a := testApp()
	a.status.ReadOnly = true
	tt := ui.NewTester(a.composer, 900, 300)
	for _, label := range []string{"+", "Send message"} {
		tt.Move(2, 2)
		box, ok := tt.Find(label)
		if !ok {
			t.Fatalf("disabled button %q missing", label)
		}
		x, y := int(box.X+box.W/2), int(math.Ceil(float64(box.Y)))
		before := tt.Image().RGBAAt(x, y)
		tt.Move(box.X+box.W/2, box.Y+box.H/2)
		if after := tt.Image().RGBAAt(x, y); after != before {
			t.Fatalf("disabled %s reacted to hover: %v -> %v", label, before, after)
		}
	}
}
