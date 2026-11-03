package appsession

import (
	"strings"
	"testing"
)

func TestPointerScriptUsesOnlyNumericCoordinatesAndClosedEventKinds(t *testing.T) {
	script := pointerScript(BrowserInput{Kind: BrowserPointerDown, X: 12.25, Y: 44.5, Button: 0, Clicks: 1})
	for _, want := range []string{"elementFromPoint", "12.250", "44.500", `MouseEvent("mousedown"`, `.focus()`} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q: %s", want, script)
		}
	}
	if got := pointerScript(BrowserInput{Kind: BrowserText, Text: `');alert(1)//`}); got != "" {
		t.Fatalf("text entered pointer script: %s", got)
	}
}

func TestPointerActivationClickAndDoubleClick(t *testing.T) {
	click := pointerScript(BrowserInput{Kind: BrowserPointerUp, X: 1, Y: 2, Clicks: 1})
	if !strings.Contains(click, "e.click") || strings.Contains(click, "dblclick") {
		t.Fatalf("click script = %s", click)
	}
	double := pointerScript(BrowserInput{Kind: BrowserPointerUp, X: 1, Y: 2, Clicks: 2})
	if !strings.Contains(double, "dblclick") {
		t.Fatalf("double-click script = %s", double)
	}
}
