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
	tt := ui.NewTester(a.view, 1400, 800)
	if err := tt.Click("src"); err != nil || a.fileDirectory != "src" {
		t.Fatalf("directory did not open: path=%q err=%v", a.fileDirectory, err)
	}
	a.fileSelected = "src/main.go"
	a.filePreview = appsession.ProjectFilePreview{Path: "src/main.go", Text: "package main\n\nfunc main() {}\n"}
	tt = ui.NewTester(a.view, 1400, 800)
	for _, want := range []string{"Files", "main.go", "src/main.go", "File editor"} {
		if !tt.HasText(want) {
			t.Fatalf("Files drawer is missing %q; texts=%q", want, tt.Texts())
		}
	}
	if a.fileEditor == nil || a.fileEditor.Buffer.String() != a.filePreview.Text || a.fileEditor.Buffer.Language() == nil || a.fileEditor.Buffer.Language().Name != "Go" {
		t.Fatal("file preview did not use a Go syntax-highlighted editor")
	}
	if err := tt.Click("‹"); err != nil || a.fileDirectory != "" {
		t.Fatalf("parent did not open: path=%q err=%v", a.fileDirectory, err)
	}
	if err := tt.Click("Show hidden files"); err != nil || !a.fileShowHidden {
		t.Fatalf("hidden-file control did not toggle: show=%t err=%v", a.fileShowHidden, err)
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

func TestFileEditorKeepsUnsavedChangesUntilDiscarded(t *testing.T) {
	a := testApp()
	a.filesOpen, a.fileSelected = true, "main.go"
	a.filePreview = appsession.ProjectFilePreview{Path: "main.go", Text: "package main\n"}
	a.initFileEditor()
	tt := ui.NewTester(a.view, 1400, 800)
	if err := tt.Click("File editor"); err != nil {
		t.Fatalf("cannot focus file editor: %v", err)
	}
	tt.Type("x")
	if !a.fileHasChanges() {
		t.Fatal("editing did not mark the file dirty")
	}
	a.enterFileDirectory("src")
	if a.fileDirectory != "" || a.fileSelected != "main.go" || a.filesErr == "" {
		t.Fatal("navigation discarded unsaved changes")
	}
	tt.Frame()
	if err := tt.Click("Discard"); err != nil || a.fileHasChanges() || a.fileEditor.Buffer.String() != "package main\n" {
		t.Fatalf("discard did not restore preview: err=%v", err)
	}
}
