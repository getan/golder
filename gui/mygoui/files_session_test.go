package mygoui

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFileExplorerBelongsToConversationAndRestoresDirtyEditor(t *testing.T) {
	a, first, project := sessionNavigationApp(t)
	if err := os.WriteFile(filepath.Join(project, "one.txt"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	a.openFileExplorer()
	a.loadFilePreview("one.txt")
	if a.fileEditor == nil {
		t.Fatalf("preview missing: %s", a.filesErr)
	}
	a.fileEditor.Buffer.Set("unsaved draft")
	a.fileShowHidden = true
	oldScope := a.fileScope()
	second, err := a.hub.NewSession(context.Background(), project)
	if err != nil {
		t.Fatal(err)
	}
	a.activateSession(second)
	if a.fileSelected != "" || a.fileShowHidden || a.fileEditor != nil || !sameProjectPath(a.fileBrowseRoot(), project) {
		t.Fatal("same-project conversations share explorer state")
	}
	a.openFileExplorer()
	a.fileShowHidden = false
	a.activateSession(first)
	if !a.fileShowHidden || a.fileSelected != "one.txt" || a.fileEditor == nil || a.fileEditor.Buffer.String() != "unsaved draft" || !a.fileHasChanges() {
		t.Fatal("conversation did not recover its independent editor draft")
	}
	if oldScope.matches(a) {
		t.Fatal("old async callback became valid after A → B → A")
	}
	// Sidebar filtering alone cannot redirect the active conversation's files.
	a.selectProject(t.TempDir())
	if !sameProjectPath(a.project, project) || !sameProjectPath(a.fileBrowseRoot(), project) {
		t.Fatal("project selection redirected conversation files")
	}
}

func TestFileExplorerStartsAtNewConversationCwdAndCanGoAboveIt(t *testing.T) {
	a, first, project := sessionNavigationApp(t)
	a.openFileExplorer()
	a.parentFileDirectory()
	if !sameProjectPath(a.fileBrowseRoot(), filepath.Dir(project)) || !sameProjectPath(first.Snapshot().Project, project) {
		t.Fatal("parent navigation changed conversation cwd or stayed confined")
	}
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "other.txt"), []byte("other project"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := a.hub.NewSession(context.Background(), other)
	if err != nil {
		t.Fatal(err)
	}
	a.activateSession(second)
	a.openFileExplorer()
	if !sameProjectPath(a.fileBrowseRoot(), other) || len(a.fileEntries[""]) != 1 || a.fileEntries[""][0].Name != "other.txt" {
		t.Fatalf("new conversation did not use its cwd: root=%s entries=%+v error=%s", a.fileBrowseRoot(), a.fileEntries[""], a.filesErr)
	}
	a.activateSession(first)
	if !sameProjectPath(a.fileBrowseRoot(), filepath.Dir(project)) {
		t.Fatal("parent browsing state did not remain with its conversation")
	}
}
