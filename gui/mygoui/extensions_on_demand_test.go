package mygoui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestLarkOnDemandStateExplainsMultiSessionReadiness(t *testing.T) {
	a := testApp()
	a.extension.Installed, a.extension.Enabled, a.extension.Trusted = true, true, true
	a.extension.State = "on demand"
	tt := ui.NewTester(a.larkExtensionCard, 1000, 900)
	if !tt.HasText("on demand") || !tt.HasText("Other idle conversations do not hold the shared login state lock") {
		t.Fatalf("on-demand source was presented as disconnected: %q", tt.Texts())
	}
}
