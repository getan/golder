package mygoui

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
)

const filesWidth = 620

func (a *App) resetFileExplorer() {
	a.filesOpen = false
	a.fileEntries = map[string][]appsession.ProjectFileEntry{}
	a.fileExpanded = map[string]bool{}
	a.fileLoading = map[string]bool{}
	a.fileSelected = ""
	a.filePreview = appsession.ProjectFilePreview{}
	a.filePreviewBusy = false
	a.filesErr = ""
}

func (a *App) openFileExplorer() {
	a.filesOpen = true
	a.changesOpen = false
	a.closeBrowserPanel()
	if a.fileEntries == nil {
		a.fileEntries = map[string][]appsession.ProjectFileEntry{}
	}
	if a.fileExpanded == nil {
		a.fileExpanded = map[string]bool{}
	}
	if a.fileLoading == nil {
		a.fileLoading = map[string]bool{}
	}
	if _, loaded := a.fileEntries[""]; !loaded {
		a.loadFileDirectory("")
	}
}

func (a *App) loadFileDirectory(relative string) {
	if a.fileLoading[relative] || a.ctrl == nil {
		return
	}
	a.fileLoading[relative] = true
	project, win := a.project, a.win
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		type result struct {
			entries []appsession.ProjectFileEntry
			err     error
		}
		ch := make(chan result, 1)
		go func() {
			entries, err := a.ctrl.ListProjectFiles(relative)
			ch <- result{entries: entries, err: err}
		}()
		var got result
		select {
		case got = <-ch:
		case <-ctx.Done():
			got.err = ctx.Err()
		}
		apply := func() {
			if a.project != project {
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
	a.fileSelected, a.filePreviewBusy = path, true
	a.filePreview = appsession.ProjectFilePreview{}
	project, win := a.project, a.win
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		type result struct {
			preview appsession.ProjectFilePreview
			err     error
		}
		ch := make(chan result, 1)
		go func() {
			preview, err := a.ctrl.ReadProjectFile(path)
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
		}
		if win == nil {
			apply()
			return
		}
		win.Update(apply)
	}()
}

func (a *App) fileExplorerPanel(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Width(filesWidth).MinWidth(380).BorderWidth(0, 0, 0, 1).BorderColor(t.Border).Children(func() {
		ui.Row(c).Height(42).Padding(6, 10).Gap(8).AlignItems(ui.Center).
			BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			ui.Icon(c, filesMonochromeIcon).Size(15, 15)
			ui.Text(c, "Files").FontSize(13).FontWeight(600)
			ui.Text(c, filepath.Base(a.project)).FontSize(11).TextColor(t.TextMuted).Grow(1).SingleLine().Ellipsis("…").Tooltip(a.project)
			if ui.Button(c, "↻").Tooltip("Refresh project files").Clicked() {
				a.fileEntries = map[string][]appsession.ProjectFileEntry{}
				a.fileExpanded = map[string]bool{}
				a.loadFileDirectory("")
			}
			if ui.Button(c, "×").Tooltip("Close Files").Clicked() {
				a.filesOpen = false
			}
		})
		if a.filesErr != "" {
			ui.Text(c, a.filesErr).Padding(6, 10).FontSize(11).TextColor(t.Warning).Wrap()
		}
		ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
			ui.Column(c).Width(250).MinWidth(190).BorderWidth(0, 1, 0, 0).BorderColor(t.Border).Children(func() {
				ui.Scroll(c).Grow(1).MinHeight(0).Padding(6).Children(func() {
					a.renderFileDirectory(c, "", 0)
				})
			})
			ui.Column(c).Grow(1).MinWidth(0).Children(func() {
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
		if entry.Path == a.fileSelected {
			row.Background(t.SurfacePressed)
		} else if row.Hovered() {
			row.Background(t.SurfaceHover)
		}
		row.Children(func() {
			ui.Spacer(c).Width(float32(depth) * 14)
			if entry.Dir && !entry.Symlink {
				chevron := "›"
				if a.fileExpanded[entry.Path] {
					chevron = "⌄"
				}
				ui.Text(c, chevron).Width(13).FontSize(11).TextColor(t.TextMuted).TextAlign(ui.Center)
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
				a.fileExpanded[entry.Path] = !a.fileExpanded[entry.Path]
				if a.fileExpanded[entry.Path] {
					a.loadFileDirectory(entry.Path)
				}
			default:
				a.loadFilePreview(entry.Path)
			}
		}
		if entry.Dir && a.fileExpanded[entry.Path] {
			if depth >= 20 {
				ui.Text(c, "Depth limit reached").Padding(3, 8+float32(depth+1)*14).FontSize(10).TextColor(t.TextMuted)
			} else {
				a.renderFileDirectory(c, entry.Path, depth+1)
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
		}
	})
	switch {
	case a.filePreviewBusy:
		ui.Text(c, "Loading preview…").Padding(12).FontSize(11).TextColor(t.TextMuted)
	case a.filePreview.Binary:
		ui.Text(c, "Binary file — preview is unavailable.").Padding(12).FontSize(12).TextColor(t.TextMuted)
	default:
		text := a.filePreview.Text
		if text == "" {
			text = "Empty file"
		}
		ui.Scroll(c).Grow(1).MinHeight(0).Padding(10, 12).Children(func() {
			ui.Text(c, text).Font("monospace").FontSize(11.5).TextColor(t.Text).Selectable().Wrap()
		})
	}
}
