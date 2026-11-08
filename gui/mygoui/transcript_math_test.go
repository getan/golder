package mygoui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/getan/golder/gui/appsession"
)

func TestTranscriptMathRenderingAndLimits(t *testing.T) {
	m := newTranscriptMath()
	for _, tex := range []string{`x`, `2`, `x^2`, `x_i`, `x^{12}`, `x^{1+2}`, `x_i^2`, `E=mc^2`, `\sum_{i=1}^{n}`, `\frac{x_i^2}{\sqrt{1+y_j}}`, `\begin{bmatrix}1&x_i\\2&y_j\end{bmatrix}`, `\begin{pmatrix}1&2\\3&4\end{pmatrix}`, `\frac{a}{b}`, `\sqrt{x}`, `\alpha + \beta`} {
		img := m.render(tex, 16)
		if img == nil || img.w <= 0 || img.h <= 0 {
			t.Errorf("formula %q did not render", tex)
		}
		if m.render(tex, 16) != img {
			t.Errorf("formula %q was not cached", tex)
		}
	}
	for _, tex := range []string{"", strings.Repeat("x", maxTranscriptMathBytes+1), "{unclosed", strings.Repeat("{", 17) + "x" + strings.Repeat("}", 17), `\notarealcommand{a}`, `x^{\notarealcommand}`, `\begin{matrix}1&2\\3\end{matrix}`, `\begin{matrix}1\end{pmatrix}`} {
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

func TestComplexMathMalformedAndBounded(t *testing.T) {
	cases := []string{
		`x^`, `x_`, `x^^2`, `x_{i`, `x_i_i`, `\frac{x_1}`, `\sqrt{x_1`,
		`\sqrt[3]{x_i}`, `x^{\sqrt[3]{a}}`,
		`\begin{matrix}1&2\\3\end{matrix}`, `\begin{matrix}1\end{pmatrix}`,
		`\begin{matrix}1&\begin{matrix}2\end{matrix}\end{matrix}`,
		`\frac{x_1}{\notarealcommand}`, strings.Repeat(`x_1+`, 129),
	}
	for _, tex := range cases {
		t.Run(tex, func(t *testing.T) {
			if got := newTranscriptMath().render(tex, 16); got != nil {
				t.Fatalf("unsupported or invalid expression was rendered: %q", tex)
			}
		})
	}
}

func TestComplexMathCompositesContainVisiblePixels(t *testing.T) {
	for _, tex := range []string{`\frac{x_i^2}{\sqrt{1+y_j}}`, `\sum_{i=1}^{n}`, `\begin{pmatrix}1&2\\3&4\end{pmatrix}`} {
		img := newTranscriptMath().render(tex, 18)
		if img == nil || img.mask == nil {
			t.Fatalf("%q did not render", tex)
		}
		visible := 0
		for y := 0; y < img.mask.Bounds().Dy(); y++ {
			for x := 0; x < img.mask.Bounds().Dx(); x++ {
				if img.mask.NRGBAAt(x, y).A > 0 {
					visible++
				}
			}
		}
		if visible < 10 {
			t.Fatalf("%q rendered an empty image", tex)
		}
	}
}

func TestComplexMathDisplayedInTranscript(t *testing.T) {
	a := testApp()
	tex := `\frac{x_i^2}{\sqrt{1+y_j}}`
	item := appsession.Item{ID: "complex-math", Kind: appsession.ItemAssistant, Done: true, Text: "$$\n" + tex + "\n$$"}
	tt := ui.NewTester(func(c *ui.Context) { a.itemView(c, item) }, 800, 400)
	box, ok := tt.Find("Math formula preview")
	if !ok || box.W <= 0 || box.H <= 0 || !tt.HasText(tex) {
		t.Fatalf("missing complex preview or source: box=%+v, found=%v, text=%q", box, ok, tt.Texts())
	}
}
