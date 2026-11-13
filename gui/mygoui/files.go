package mygoui

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"github.com/getan/golder/gui/appsession"
	"github.com/getan/golder/internal/displaytext"
)

func (a *App) resetFileExplorer() {
	a.filesOpen = false
	a.fileGeneration++
	a.fileDirectory = ""
	a.fileRoot = a.project
	a.fileShowHidden = false
	a.fileEntries = map[string][]appsession.ProjectFileEntry{}
	a.fileLoading = map[string]bool{}
	a.fileSelected = ""
	a.filePreview = appsession.ProjectFilePreview{}
	a.filePretty = false
	a.filePreviewBusy = false
	a.fileEditor = nil
	a.fileSavedVersion = 0
	a.fileSaving = false
	a.filesErr = ""
}

func (a *App) openFileExplorer() {
	a.filesOpen = true
	a.closeChangesPanel()
	if a.fileEntries == nil {
		a.fileEntries = map[string][]appsession.ProjectFileEntry{}
	}
	if a.fileLoading == nil {
		a.fileLoading = map[string]bool{}
	}
	if _, loaded := a.fileEntries[a.fileDirectory]; !loaded {
		a.loadFileDirectory(a.fileDirectory)
	}
}

func (a *App) refreshFileDirectory() {
	a.fileGeneration++
	a.fileEntries = map[string][]appsession.ProjectFileEntry{}
	a.fileLoading = map[string]bool{}
	a.loadFileDirectory(a.fileDirectory)
	if a.filePreviewBusy {
		a.filePreviewBusy = false
		a.loadFilePreview(a.fileSelected)
	}
}

func (a *App) enterFileDirectory(relative string) {
	if a.fileSaving || a.fileHasChanges() {
		a.filesErr = "Save or discard the current file's changes before changing folders."
		return
	}
	a.fileGeneration++
	a.fileLoading = map[string]bool{}
	a.fileDirectory = relative
	a.fileSelected = ""
	a.filePreview = appsession.ProjectFilePreview{}
	a.filePretty = false
	a.filePreviewBusy = false
	a.fileEditor = nil
	a.filesErr = ""
	if _, loaded := a.fileEntries[relative]; !loaded {
		a.loadFileDirectory(relative)
	}
}

func (a *App) loadFileDirectory(relative string) {
	if a.fileLoading[relative] || a.ctrl == nil {
		return
	}
	if a.fileLoading == nil {
		a.fileLoading = map[string]bool{}
	}
	a.fileLoading[relative] = true
	scope, win, showHidden := a.fileScope(), a.win, a.fileShowHidden
	browser, err := scope.ctrl.FileBrowserAt(scope.session, scope.project, scope.root)
	if err != nil {
		delete(a.fileLoading, relative)
		a.filesErr = err.Error()
		return
	}
	load := func() {
		entries, err := browser.List(relative, showHidden)
		apply := func() {
			if !scope.matches(a) {
				return
			}
			delete(a.fileLoading, relative)
			if err != nil {
				a.filesErr = err.Error()
				return
			}
			a.filesErr = ""
			a.fileEntries[relative] = entries
		}
		if win == nil {
			apply()
		} else {
			win.Update(apply)
		}
	}
	if win == nil {
		load()
	} else {
		go load()
	}
}

func (a *App) loadFilePreview(path string) {
	if a.filePreviewBusy || a.ctrl == nil {
		return
	}
	if a.fileSaving || a.fileHasChanges() {
		a.filesErr = "Save or discard the current file's changes before opening another file."
		return
	}
	a.fileSelected, a.filePreviewBusy = path, true
	a.filePreview = appsession.ProjectFilePreview{}
	a.filePretty = false
	a.fileEditor = nil
	scope, win := a.fileScope(), a.win
	browser, err := scope.ctrl.FileBrowserAt(scope.session, scope.project, scope.root)
	if err != nil {
		a.filePreviewBusy = false
		a.filesErr = err.Error()
		return
	}
	load := func() {
		preview, err := browser.Read(path)
		apply := func() {
			if !scope.matches(a) || a.fileSelected != path {
				return
			}
			a.filePreviewBusy = false
			if err != nil {
				a.filesErr = err.Error()
				return
			}
			a.filesErr = ""
			a.filePreview = preview
			a.filePretty = isJSONFile(path)
			a.initFileEditor()
		}
		if win == nil {
			apply()
		} else {
			win.Update(apply)
		}
	}
	if win == nil {
		load()
	} else {
		go load()
	}
}

func (a *App) initFileEditor() {
	if a.filePreview.Binary || a.filePreviewBusy {
		return
	}
	text := a.filePreview.Text
	if a.filePretty {
		trimmed := strings.TrimSpace(text)
		if a.filePreview.Truncated || len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') || !json.Valid([]byte(trimmed)) {
			a.filePretty = false
		} else if pretty := displaytext.PrettyJSON(text); pretty != "" {
			text = pretty
		}
	}
	buffer := editor.NewBuffer(text)
	buffer.SetLanguage(editor.LanguageFor(a.fileSelected))
	a.fileEditor = &editor.State{Buffer: buffer, SoftWrap: true, ReadOnly: a.filePreview.Truncated || a.filePretty, Label: "File editor"}
	a.fileSavedVersion = buffer.Version()
}

func isJSONFile(path string) bool {
	return strings.EqualFold(filepath.Ext(strings.TrimSpace(path)), ".json")
}

func (a *App) fileHasChanges() bool {
	return a.fileEditor != nil && a.fileEditor.Buffer != nil && a.fileEditor.Buffer.Version() != a.fileSavedVersion
}

func (a *App) saveFile() {
	if a.ctrl == nil || a.fileEditor == nil || a.fileEditor.Buffer == nil || a.filePreview.Truncated || a.filePretty || a.fileSaving || !a.fileHasChanges() {
		return
	}
	path, scope, win := a.fileSelected, a.fileScope(), a.win
	browser, err := scope.ctrl.FileBrowserAt(scope.session, scope.project, scope.root)
	if err != nil {
		a.filesErr = err.Error()
		return
	}
	var content bytes.Buffer
	if _, err := a.fileEditor.Buffer.WriteTo(&content); err != nil {
		a.filesErr = err.Error()
		return
	}
	text, version, digest := content.String(), a.fileEditor.Buffer.Version(), a.filePreview.Digest
	a.fileSaving, a.filesErr = true, ""
	save := func() {
		newDigest, err := browser.Save(path, digest, text)
		apply := func() {
			// Refreshing the directory does not invalidate an in-flight save.
			if !scope.matchesLocation(a) || a.fileSelected != path {
				return
			}
			a.fileSaving = false
			if err != nil {
				a.filesErr = err.Error()
				return
			}
			a.filePreview.Text, a.filePreview.Digest = text, newDigest
			a.fileSavedVersion = version
			a.filesErr = ""
		}
		if win == nil {
			apply()
		} else {
			win.Update(apply)
		}
	}
	if win == nil {
		save()
	} else {
		go save()
	}
}

func (a *App) fileExplorerPanel(c *ui.Context) {
	t := c.Theme()
	if c.Shortcut(ui.Cmd|ui.Shift, ui.KeyPeriod) {
		a.fileShowHidden = !a.fileShowHidden
		a.refreshFileDirectory()
	}
	if c.Shortcut(ui.Cmd, ui.KeyS) {
		a.saveFile()
	}
	ui.Column(c.Key("files:"+a.status.SessionID)).Fill().MinWidth(0).BorderWidth(0, 0, 0, 1).BorderColor(t.Border).Children(func() {
		ui.Row(c).Height(42).Padding(6, 10).Gap(8).AlignItems(ui.Center).
			BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			ui.Icon(c, filesMonochromeIcon).Size(15, 15)
			ui.Text(c, "Files").FontSize(13).FontWeight(600)
			root := a.fileBrowseRoot()
			if subtleButton(c, "‹", a.fileDirectory == "" && filepath.Dir(root) == root).Label("Parent folder").Tooltip("Parent folder").Clicked() {
				a.parentFileDirectory()
			}
			if subtleButton(c, "⌂").Label("Conversation working folder").Tooltip(a.project).Clicked() {
				if !a.fileSaving && !a.fileHasChanges() {
					a.resetFileExplorer()
					a.openFileExplorer()
				}
			}
			label := filepath.Join(root, a.fileDirectory)
			ui.Text(c, label).FontSize(11).TextColor(t.TextMuted).Grow(1).SingleLine().Ellipsis("…").Tooltip(label)
			hiddenLabel := "Show hidden files"
			if a.fileShowHidden {
				hiddenLabel = "Hide hidden files"
			}
			if compactIconButton(c, "files-hidden", hiddenFilesIcon, hiddenLabel, hiddenLabel+" (⌘⇧.)").Clicked() {
				a.fileShowHidden = !a.fileShowHidden
				a.refreshFileDirectory()
			}
			if subtleButton(c, "↻").Tooltip("Refresh project files").Clicked() {
				a.refreshFileDirectory()
			}
			if subtleButton(c, "×").Tooltip("Close Files").Clicked() {
				a.filesOpen = false
			}
		})
		if a.filesErr != "" {
			ui.Text(c, a.filesErr).Padding(6, 10).FontSize(11).TextColor(t.Warning).Wrap()
		}
		ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).BorderWidth(0, 1, 0, 0).BorderColor(t.Border).Children(func() {
				ui.Scroll(c).Grow(1).MinHeight(0).Padding(6).Children(func() {
					a.renderFileDirectory(c, a.fileDirectory, 0)
				})
			})
			ui.Column(c).Grow(2).MinWidth(0).Children(func() {
				a.filePreviewView(c)
			})
		})
	})
}

func (a *App) renderFileDirectory(c *ui.Context, relative string, depth int) {
	t := c.Theme()
	entries, loaded := a.fileEntries[relative]
	if !loaded {
		message := "Loading…"
		if !a.fileLoading[relative] {
			message = "Not loaded"
		}
		ui.Text(c, message).Padding(4, 8+float32(depth)*14).FontSize(11).TextColor(t.TextMuted)
		return
	}
	for _, entry := range entries {
		entry := entry
		row := ui.ButtonBase(c.Key("project-file:"+entry.Path)).FillWidth().MinHeight(26).Radius(5).
			Padding(3, 7).Justify(ui.Start).Label(entry.Name).Description(entry.Path)
		hoverBorder(c, row)
		if entry.Path == a.fileSelected {
			row.Background(t.SurfacePressed)
		} else if row.Hovered() {
			row.Background(t.SurfaceHover)
		}
		row.Children(func() {
			ui.Box(c).Width(float32(depth) * 14).Shrink(0)
			if entry.Dir && !entry.Symlink {
				ui.Text(c, "›").Width(13).FontSize(11).TextColor(t.TextMuted).TextAlign(ui.Center)
				ui.Icon(c, folderMonochromeIcon).Size(14, 14)
			} else {
				ui.Box(c).Width(13).Shrink(0)
				ui.Icon(c, filesMonochromeIcon).Size(14, 14)
			}
			ui.Text(c, entry.Name).Grow(1).FontSize(11.5).TextColor(t.Text).SingleLine().Ellipsis("…")
			if entry.Symlink {
				ui.Text(c, "link").FontSize(9.5).TextColor(t.TextMuted)
			}
		})
		if row.Clicked() {
			switch {
			case entry.Symlink:
				a.filesErr = "Symbolic links are shown but not followed."
			case entry.Dir:
				a.enterFileDirectory(entry.Path)
			default:
				a.loadFilePreview(entry.Path)
			}
		}
	}
}

func (a *App) filePreviewView(c *ui.Context) {
	t := c.Theme()
	path := strings.TrimSpace(a.fileSelected)
	if path == "" {
		ui.Column(c).Grow(1).Center().Gap(6).Children(func() {
			ui.Icon(c, filesMonochromeIcon).Size(24, 24).TextColor(t.TextMuted)
			ui.Text(c, "Select a file to preview").FontSize(12).TextColor(t.TextMuted)
		})
		return
	}
	ui.Row(c).Height(38).Padding(6, 10).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		ui.Text(c, path).FontSize(11.5).FontWeight(600).Grow(1).SingleLine().Ellipsis("…").Tooltip(path)
		if !a.filePreviewBusy && !a.filePreview.Binary && isJSONFile(path) {
			label := "Pretty"
			if a.filePretty {
				label = "Raw"
			}
			if subtleButton(c, label, a.fileSaving || a.fileHasChanges() || a.filePreview.Truncated).Tooltip("Toggle formatted JSON preview").Clicked() {
				a.filePretty = !a.filePretty
				a.initFileEditor()
			}
		}
		if a.filePreview.Truncated {
			ui.Text(c, "preview truncated").FontSize(10).TextColor(t.Warning)
		} else if a.fileHasChanges() {
			if subtleButton(c, "Discard", a.fileSaving).Clicked() {
				a.initFileEditor()
				a.filesErr = ""
			}
		}
		if subtlePrimaryButton(c, "Save", a.fileSaving || a.ctrl == nil || a.filePreviewBusy || a.filePreview.Binary || a.filePreview.Truncated || a.filePretty || !a.fileHasChanges()).Clicked() {
			a.saveFile()
		}
	})
	switch {
	case a.filePreviewBusy:
		ui.Text(c, "Loading preview…").Padding(12).FontSize(11).TextColor(t.TextMuted)
	case a.filePreview.Binary:
		ui.Text(c, "Binary file — preview is unavailable.").Padding(12).FontSize(12).TextColor(t.TextMuted)
	default:
		if a.fileEditor == nil {
			a.initFileEditor()
		}
		if a.fileEditor != nil {
			editor.View(c, a.fileEditor).Grow(1).MinHeight(0)
		}
	}
}
