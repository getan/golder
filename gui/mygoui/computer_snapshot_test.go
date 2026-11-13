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
	if a.toolOpen["snapshot"] || tt.HasText("snapshot-data") || tt.HasText("END-OF-SNAPSHOT") || tt.HasText("Expand · Ctrl+T") {
		t.Fatalf("snapshot did not start folded: %q", tt.Texts())
	}
	if err := tt.Click("computer · snapshot · done"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(250 * time.Millisecond)
	if !a.toolOpen["snapshot"] || !tt.HasText("END-OF-SNAPSHOT") {
		t.Fatal("click did not expand full snapshot")
	}
	if err := tt.Click("computer · snapshot · done"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(250 * time.Millisecond)
	if a.toolOpen["snapshot"] || tt.HasText("snapshot-data") || tt.HasText("END-OF-SNAPSHOT") {
		t.Fatal("click did not completely fold snapshot")
	}
	tt.Key(ui.Ctrl, ui.KeyT)
	tt.Advance(250 * time.Millisecond)
	if !a.toolOpen["snapshot"] || !tt.HasText("END-OF-SNAPSHOT") {
		t.Fatal("Ctrl+T did not expand snapshot")
	}
	tt.Key(ui.Ctrl, ui.KeyT)
	tt.Advance(250 * time.Millisecond)
	if a.toolOpen["snapshot"] || tt.HasText("snapshot-data") || tt.HasText("END-OF-SNAPSHOT") {
		t.Fatal("Ctrl+T did not completely fold snapshot")
	}
}

func TestGUISnapshotsStartCompletelyFolded(t *testing.T) {
	for _, name := range []string{"computer", "browser"} {
		t.Run(name, func(t *testing.T) {
			a := testApp()
			a.items = append(a.items, exploreTestTool("snapshot", name, map[string]any{"action": "snapshot"}, "first-row\nsecond-row\nthird-row\nfourth-row"))
			tt := ui.NewTester(a.view, 1200, 900)
			if tt.HasText("first-row") || tt.HasText("Expand · Ctrl+T") {
				t.Fatalf("folded snapshot leaked its body: %q", tt.Texts())
			}
			if err := tt.Click(name + " · snapshot · done"); err != nil {
				t.Fatal(err)
			}
			tt.Advance(250 * time.Millisecond)
			if !tt.HasText("first-row") || !tt.HasText("fourth-row") {
				t.Fatal("headline click did not expose snapshot body")
			}
		})
	}
}
