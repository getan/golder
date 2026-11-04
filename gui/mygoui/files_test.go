package mygoui

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
)

func TestFilesDrawerRendersTreeAndPreview(t *testing.T) {
	a := testApp()
	a.filesOpen = true
	a.fileEntries[""] = []appsession.ProjectFileEntry{
		{Name: "src", Path: "src", Dir: true},
		{Name: "README.md", Path: "README.md", Size: 20},
	}
	a.fileEntries["src"] = []appsession.ProjectFileEntry{{Name: "main.go", Path: "src/main.go", Size: 40}}
	a.fileExpanded["src"] = true
	a.fileSelected = "src/main.go"
	a.filePreview = appsession.ProjectFilePreview{Path: "src/main.go", Text: "package main\n\nfunc main() {}\n"}
	tt := ui.NewTester(a.view, 1400, 800)
	for _, want := range []string{"Files", "src", "README.md", "main.go", "src/main.go", "package main\n\nfunc main() {}\n"} {
		if !tt.HasText(want) {
			t.Fatalf("Files drawer is missing %q; texts=%q", want, tt.Texts())
		}
	}
	if err := tt.Click("Files"); err != nil || a.filesOpen {
		t.Fatalf("Files action did not close drawer: open=%t err=%v", a.filesOpen, err)
	}
}

func TestFileExplorerMutuallyExcludesOtherDrawers(t *testing.T) {
	a := testApp()
	a.changesOpen = true
	a.browserPanelOpen = true
	a.openFileExplorer()
	if !a.filesOpen || a.changesOpen || a.browserPanelOpen {
		t.Fatalf("drawer state files=%t changes=%t browser=%t", a.filesOpen, a.changesOpen, a.browserPanelOpen)
	}
}
