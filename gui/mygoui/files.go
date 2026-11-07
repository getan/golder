package mygoui

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"github.com/getan/golder/gui/appsession"
)

func (a *App) resetFileExplorer() {
	a.filesOpen = false
	a.fileGeneration++
	a.fileDirectory = ""
	a.fileShowHidden = false
	a.fileEntries = map[string][]appsession.ProjectFileEntry{}
	a.fileLoading = map[string]bool{}
	a.fileSelected = ""
	a.filePreview = appsession.ProjectFilePreview{}
	a.filePreviewBusy = false
	a.fileEditor = nil
	a.fileSavedVersion = 0
	a.fileSaving = false
	a.filesErr = ""
}

func (a *App) openFileExplorer() {
	a.filesOpen = true
	a.changesOpen = false
	a.hideComputerPanel()
	a.closeBrowserPanel()
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
}

func (a *App) enterFileDirectory(relative string) {
	if a.fileSaving || a.fileHasChanges() {
		a.filesErr = "Save or discard the current file's changes before changing folders."
		return
	}
	a.fileDirectory = relative
	a.fileSelected = ""
	a.filePreview = appsession.ProjectFilePreview{}
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
	a.fileLoading[relative] = true
	project, win, generation, showHidden, ctrl := a.project, a.win, a.fileGeneration, a.fileShowHidden, a.ctrl
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		type result struct {
			entries []appsession.ProjectFileEntry
			err     error
		}
		ch := make(chan result, 1)
		go func() {
			entries, err := ctrl.ListProjectFilesWithHidden(relative, showHidden)
			ch <- result{entries: entries, err: err}
		}()
		var got result
		select {
		case got = <-ch:
		case <-ctx.Done():
			got.err = ctx.Err()
		}
		apply := func() {
			if a.project != project || a.fileGeneration != generation {
				return
			}
			delete(a.fileLoading, relative)
			if got.err != nil {
				a.filesErr = got.err.Error()
				return
			}
			a.filesErr = ""
			a.fileEntries[relative] = got.entries
		}
		if win == nil {
			apply()
			return
		}
		win.Update(apply)
	}()
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
	a.fileEditor = nil
	project, win, ctrl := a.project, a.win, a.ctrl
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		type result struct {
			preview appsession.ProjectFilePreview
			err     error
		}
		ch := make(chan result, 1)
		go func() {
			preview, err := ctrl.ReadProjectFile(path)
			ch <- result{preview: preview, err: err}
		}()
		var got result
		select {
		case got = <-ch:
		case <-ctx.Done():
			got.err = ctx.Err()
		}
		apply := func() {
			if a.project != project || a.fileSelected != path {
				return
			}
			a.filePreviewBusy = false
			if got.err != nil {
				a.filesErr = got.err.Error()
				return
			}
			a.filesErr = ""
			a.filePreview = got.preview
			a.initFileEditor()
		}
		if win == nil {
			apply()
			return
		}
		win.Update(apply)
	}()
}

func (a *App) initFileEditor() {
	if a.filePreview.Binary || a.filePreviewBusy {
		return
	}
	buffer := editor.NewBuffer(a.filePreview.Text)
	buffer.SetLanguage(editor.LanguageFor(a.fileSelected))
	a.fileEditor = &editor.State{Buffer: buffer, SoftWrap: true, ReadOnly: a.filePreview.Truncated, Label: "File editor"}
	a.fileSavedVersion = buffer.Version()
}

func (a *App) fileHasChanges() bool {
	return a.fileEditor != nil && a.fileEditor.Buffer != nil && a.fileEditor.Buffer.Version() != a.fileSavedVersion
}

func (a *App) saveFile() {
	if a.ctrl == nil || a.fileEditor == nil || a.fileEditor.Buffer == nil || a.filePreview.Truncated || a.fileSaving || !a.fileHasChanges() {
		return
	}
	path, project, win, ctrl := a.fileSelected, a.project, a.win, a.ctrl
	var content bytes.Buffer
	if _, err := a.fileEditor.Buffer.WriteTo(&content); err != nil {
		a.filesErr = err.Error()
		return
	}
	text, version, digest := content.String(), a.fileEditor.Buffer.Version(), a.filePreview.Digest
	a.fileSaving, a.filesErr = true, ""
	go func() {
		newDigest, err := ctrl.SaveProjectFile(project, path, digest, text)
		apply := func() {
			if a.project != project || a.fileSelected != path {
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
	}()
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
	ui.Column(c).Fill().MinWidth(0).BorderWidth(0, 0, 0, 1).BorderColor(t.Border).Children(func() {
		ui.Row(c).Height(42).Padding(6, 10).Gap(8).AlignItems(ui.Center).
			BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			ui.Icon(c, filesMonochromeIcon).Size(15, 15)
			ui.Text(c, "Files").FontSize(13).FontWeight(600)
			if subtleButton(c, "‹", a.fileDirectory == "").Tooltip("Parent folder").Clicked() {
				parent := filepath.ToSlash(filepath.Dir(a.fileDirectory))
				if parent == "." {
					parent = ""
				}
				a.enterFileDirectory(parent)
			}
			label := filepath.Base(a.project)
			if a.fileDirectory != "" {
				label += " / " + a.fileDirectory
			}
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
			Padding(3, 7).Label(entry.Name).Description(entry.Path)
		hoverBorder(c, row)
		if entry.Path == a.fileSelected {
			row.Background(t.SurfacePressed)
		} else if row.Hovered() {
			row.Background(t.SurfaceHover)
		}
		row.Children(func() {
			ui.Spacer(c).Width(float32(depth) * 14)
			if entry.Dir && !entry.Symlink {
				ui.Text(c, "›").Width(13).FontSize(11).TextColor(t.TextMuted).TextAlign(ui.Center)
				ui.Icon(c, folderMonochromeIcon).Size(14, 14)
			} else {
				ui.Spacer(c).Width(13)
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
		if a.filePreview.Truncated {
			ui.Text(c, "preview truncated").FontSize(10).TextColor(t.Warning)
		} else if a.fileHasChanges() {
			if subtleButton(c, "Discard", a.fileSaving).Clicked() {
				a.initFileEditor()
				a.filesErr = ""
			}
		}
		if subtlePrimaryButton(c, "Save", a.fileSaving || a.ctrl == nil || a.filePreviewBusy || a.filePreview.Binary || a.filePreview.Truncated || !a.fileHasChanges()).Clicked() {
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
