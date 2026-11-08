package mygoui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getan/golder/gui/appsession"
)

func TestTranscriptMathRenderingAndLimits(t *testing.T) {
	m := newTranscriptMath()
	for _, tex := range []string{`x`, `2`, `x^2`, `x_i`, `x^{12}`, `E=mc^2`, `\frac{a}{b}`, `\sqrt{x}`, `\alpha + \beta`} {
		img := m.render(tex, 16)
		if img == nil || img.w <= 0 || img.h <= 0 {
			t.Errorf("formula %q did not render", tex)
		}
		if m.render(tex, 16) != img {
			t.Errorf("formula %q was not cached", tex)
		}
	}
	for _, tex := range []string{"", strings.Repeat("x", maxTranscriptMathBytes+1), "{unclosed", strings.Repeat("{", 17) + "x" + strings.Repeat("}", 17), `\notarealcommand{a}`, `x^{1+2}`} {
		if img := m.render(tex, 16); img != nil {
			t.Errorf("invalid formula %q rendered", tex)
		}
	}
}

func TestTranscriptMathViewAndFallback(t *testing.T) {
	a := testApp()
	item := appsession.Item{ID: "math", Kind: appsession.ItemAssistant, Done: true, Text: "Price $5 and inline $x^2$.\n\n$$\n\\frac{a}{b}\n$$\n\n$$\n\\notarealcommand{a}\n$$"}
	tt := ui.NewTester(func(c *ui.Context) { a.itemView(c, item) }, 800, 500)
	if !tt.HasText(`\notarealcommand{a}`) {
		t.Fatalf("invalid expression missing: %q", tt.Texts())
	}
	if !tt.HasText(`\frac{a}{b}`) {
		t.Fatalf("display source missing: %q", tt.Texts())
	}
	if box, ok := tt.Find("Math formula preview"); !ok || box.W <= 0 || box.H <= 0 {
		t.Fatalf("display formula was not laid out: %v, %v", box, ok)
	}
	if tt.Image() == nil {
		t.Fatal("math did not draw")
	}
}
