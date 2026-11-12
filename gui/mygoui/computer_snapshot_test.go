package mygoui

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestGUIComputerSnapshotFoldClickAndCtrlT(t *testing.T) {
	a := testApp()
	a.items = append(a.items, exploreTestTool("snapshot", "computer", map[string]any{"action": "snapshot"}, strings.Repeat("snapshot-data ", 300)+" END-OF-SNAPSHOT"))
	tt := ui.NewTester(a.view, 1200, 900)
	if a.toolOpen["snapshot"] || tt.HasText("END-OF-SNAPSHOT") || !tt.HasText("Expand · Ctrl+T") {
		t.Fatalf("snapshot did not start folded: %q", tt.Texts())
	}
	if err := tt.Click("… 点击展开 / Expand · Ctrl+T"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(250 * time.Millisecond)
	if !a.toolOpen["snapshot"] || !tt.HasText("END-OF-SNAPSHOT") {
		t.Fatal("click did not expand full snapshot")
	}
	tt.Key(ui.Ctrl, ui.KeyT)
	tt.Advance(250 * time.Millisecond)
	if a.toolOpen["snapshot"] || tt.HasText("END-OF-SNAPSHOT") {
		t.Fatal("Ctrl+T did not fold snapshot")
	}
	tt.Key(ui.Ctrl, ui.KeyT)
	tt.Advance(250 * time.Millisecond)
	if !a.toolOpen["snapshot"] || !tt.HasText("END-OF-SNAPSHOT") {
		t.Fatal("Ctrl+T did not expand snapshot")
	}
}
