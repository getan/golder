package mygoui

import (
	"path/filepath"

	"github.com/egoist/mygo/ui/editor"
	"github.com/getan/golder/gui/appsession"
)

type fileExplorerSnapshot struct {
	project, root, directory, selected string
	open, hidden                       bool
	entries                            map[string][]appsession.ProjectFileEntry
	preview                            appsession.ProjectFilePreview
	pretty                             bool
	editor                             *editor.State
	savedVersion                       uint64
	previewPending                     bool
}

type fileViewScope struct {
	ctrl                   *appsession.Controller
	session, project, root string
	generation             uint64
}

func (a *App) fileScope() fileViewScope {
	return fileViewScope{a.ctrl, a.status.SessionID, a.project, a.fileBrowseRoot(), a.fileGeneration}
}

func (s fileViewScope) matches(a *App) bool {
	return s.matchesLocation(a) && a.fileGeneration == s.generation
}

func (s fileViewScope) matchesLocation(a *App) bool {
	return a.ctrl == s.ctrl && a.status.SessionID == s.session && a.project == s.project && a.fileBrowseRoot() == s.root
}

func (a *App) rememberFileExplorer(id, project string) {
	if a.fileStates == nil {
		a.fileStates = map[string]fileExplorerSnapshot{}
	}
	a.fileStates[id] = fileExplorerSnapshot{project: project, root: a.fileRoot, directory: a.fileDirectory, selected: a.fileSelected,
		open: a.filesOpen, hidden: a.fileShowHidden, entries: a.fileEntries, preview: a.filePreview, pretty: a.filePretty, editor: a.fileEditor, savedVersion: a.fileSavedVersion, previewPending: a.filePreviewBusy}
}

func (a *App) restoreFileExplorer(id, project string) {
	a.resetFileExplorer() // invalidate every previous async callback, even A → B → A
	s, ok := a.fileStates[id]
	if !ok || !sameProjectPath(s.project, project) {
		return
	}
	delete(a.fileStates, id)
	a.fileRoot, a.fileDirectory, a.fileSelected = s.root, s.directory, s.selected
	a.filesOpen, a.fileShowHidden = s.open, s.hidden
	a.filePretty = s.pretty
	a.fileEntries, a.filePreview, a.fileEditor, a.fileSavedVersion = s.entries, s.preview, s.editor, s.savedVersion
	if s.previewPending && a.fileSelected != "" {
		a.loadFilePreview(a.fileSelected)
	}
	if a.filesOpen {
		// A pending directory/preview load belongs to the old view generation.
		if _, loaded := a.fileEntries[a.fileDirectory]; !loaded {
			a.loadFileDirectory(a.fileDirectory)
		}
	}
}

func (a *App) fileBrowseRoot() string {
	if a.fileRoot != "" {
		return a.fileRoot
	}
	return a.project
}

func (a *App) parentFileDirectory() {
	if a.fileDirectory != "" {
		parent := filepath.ToSlash(filepath.Dir(a.fileDirectory))
		if parent == "." {
			parent = ""
		}
		a.enterFileDirectory(parent)
		return
	}
	if a.fileSaving || a.fileHasChanges() {
		a.filesErr = "Save or discard the current file's changes before changing folders."
		return
	}
	root := a.fileBrowseRoot()
	parent := filepath.Dir(root)
	if parent == root {
		return
	}
	a.resetFileExplorer()
	a.fileRoot, a.filesOpen = parent, true
	a.loadFileDirectory("")
}
