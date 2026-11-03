package mygoui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
)

const allProjectsLabel = "All projects"

type sessionGroup struct {
	Cwd      string
	Sessions []appsession.SessionSummary
}

func (a *App) projectOptions() []string {
	seen := map[string]bool{}
	out := []string{allProjectsLabel}
	if a.project != "" {
		seen[a.project] = true
		out = append(out, a.project)
	}
	for _, session := range a.sessions {
		cwd := strings.TrimSpace(session.Cwd)
		if cwd != "" && !seen[cwd] {
			seen[cwd] = true
			out = append(out, cwd)
		}
	}
	return out
}

func (a *App) visibleSessionGroups() []sessionGroup {
	groups := map[string][]appsession.SessionSummary{}
	order := []string{}
	selected := strings.TrimSpace(a.selectedProject)
	for _, session := range a.sessions {
		if session.Archived && !a.showArchived {
			continue
		}
		cwd := strings.TrimSpace(session.Cwd)
		if selected != "" && selected != allProjectsLabel && cwd != selected {
			continue
		}
		if _, ok := groups[cwd]; !ok {
			order = append(order, cwd)
		}
		groups[cwd] = append(groups[cwd], session)
	}
	// The active project is the first group; ListSessions already orders rows by
	// current/recent, so no second row sort is needed.
	for i, cwd := range order {
		if cwd == a.project && i != 0 {
			copy(order[1:i+1], order[0:i])
			order[0] = cwd
			break
		}
	}
	out := make([]sessionGroup, 0, len(order))
	for _, cwd := range order {
		out = append(out, sessionGroup{Cwd: cwd, Sessions: groups[cwd]})
	}
	return out
}

func (a *App) attachSessionMenu(row ui.Element, session appsession.SessionSummary) {
	row.ContextMenu(func(menu *ui.Menu) {
		if menu.Item("Open").Disabled(session.Current).Chosen() {
			a.resume(session.ID)
		}
		if menu.Item("Rename…").Chosen() {
			a.beginRename(session)
		}
		if menu.Item("Reveal project in folder").Disabled(strings.TrimSpace(session.Cwd) == "").Chosen() {
			mygo.Shell.ShowItemInFolder(session.Cwd)
		}
		if menu.Item("Copy session ID").Chosen() {
			mygo.Clipboard.WriteText(session.ID)
			a.banner, a.bannerErr = "Session ID copied.", false
		}
		menu.Submenu("Export", func(export *ui.Menu) {
			if export.Item("Markdown…").Chosen() {
				a.exportSession(session, "markdown")
			}
			if export.Item("JSONL…").Chosen() {
				a.exportSession(session, "jsonl")
			}
		})
		menu.Separator()
		archive := "Archive"
		if session.Archived {
			archive = "Unarchive"
		}
		if menu.Item(archive).Disabled(session.Current).Chosen() {
			a.archiveSession(session)
		}
		if menu.Item("Delete…").Disabled(session.Current).Chosen() {
			a.beginDelete(session)
		}
	})
}

func (a *App) beginRename(session appsession.SessionSummary) {
	a.renameID, a.renameDraft, a.renameOpen = session.ID, session.Title, true
}

func (a *App) commitRename() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := a.ctrl.RenameSession(ctx, a.renameID, a.renameDraft)
	a.renameOpen = false
	if err != nil {
		a.banner, a.bannerErr = err.Error(), true
		return
	}
	a.banner, a.bannerErr = "Session renamed.", false
	a.refreshSessions()
}

func (a *App) archiveSession(session appsession.SessionSummary) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := a.ctrl.ArchiveSession(ctx, session.ID, !session.Archived)
	if err != nil {
		a.banner, a.bannerErr = err.Error(), true
		return
	}
	a.refreshSessions()
}

func (a *App) beginDelete(session appsession.SessionSummary) {
	a.deleteID, a.deleteTitle, a.deleteOpen = session.ID, session.Title, true
}

func (a *App) commitDelete() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := a.ctrl.DeleteSession(ctx, a.deleteID)
	a.deleteOpen = false
	if err != nil {
		a.banner, a.bannerErr = err.Error(), true
		return
	}
	a.banner, a.bannerErr = "Session deleted.", false
	a.refreshSessions()
}

func (a *App) exportSession(session appsession.SessionSummary, format string) {
	win := a.win
	go func() {
		ext := ".md"
		filter := mygo.FileFilter{Name: "Markdown", Extensions: []string{"md"}}
		if format == "jsonl" {
			ext = ".jsonl"
			filter = mygo.FileFilter{Name: "Golder session", Extensions: []string{"jsonl"}}
		}
		path, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
			Parent: win, Title: "Export session", DefaultPath: safeSessionFileName(session.Title) + ext,
			ButtonLabel: "Export", Filters: []mygo.FileFilter{filter}, CreateDirectories: true,
		})
		if err == nil && path != "" {
			err = a.ctrl.ExportSession(session.ID, format, path)
		}
		if win == nil {
			return
		}
		win.Update(func() {
			if err != nil {
				a.banner, a.bannerErr = err.Error(), true
			} else if path != "" {
				a.banner, a.bannerErr = "Exported to "+path, false
			}
		})
	}()
}

func safeSessionFileName(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "golder-session"
	}
	title = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`/\\:*?"<>|`, r) {
			return '-'
		}
		return r
	}, title)
	if r := []rune(title); len(r) > 60 {
		title = string(r[:60])
	}
	title = strings.Trim(title, " .-")
	if title == "" {
		return "golder-session"
	}
	return title
}

func (a *App) sessionRenameModal(c *ui.Context) {
	ui.Modal(c, &a.renameOpen, func() {
		ui.Column(c).Width(430).Gap(12).Children(func() {
			ui.Text(c, "Rename session").FontSize(16).Bold()
			ui.TextInput(c, &a.renameDraft).AutoFocus().FillWidth().Placeholder("Session name")
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "Cancel").Clicked() {
					a.renameOpen = false
				}
				if ui.PrimaryButton(c, "Rename").Clicked() {
					a.commitRename()
				}
			})
		})
	})
}

func (a *App) sessionDeleteModal(c *ui.Context) {
	t := c.Theme()
	ui.Modal(c, &a.deleteOpen, func() {
		ui.Column(c).Width(460).Gap(12).Children(func() {
			ui.Text(c, "Delete session?").FontSize(16).Bold()
			ui.Text(c, fmt.Sprintf("%q will be permanently removed. This cannot be undone.", a.deleteTitle)).
				FontSize(12).TextColor(t.TextMuted).Wrap()
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "Cancel").Clicked() {
					a.deleteOpen = false
				}
				if ui.Button(c, "Delete").Background(t.Danger).TextColor(t.AccentText).Clicked() {
					a.commitDelete()
				}
			})
		})
	})
}

func sessionPathDisplay(path string) string {
	if path == "" {
		return "Unattributed"
	}
	if home, err := os.UserHomeDir(); err == nil {
		if relative, relErr := filepath.Rel(home, path); relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			if relative == "." {
				return "~"
			}
			return "~" + string(os.PathSeparator) + relative
		}
	}
	return path
}
