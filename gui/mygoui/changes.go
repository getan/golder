package mygoui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"github.com/getan/golder/gui/appsession"
)

func (a *App) closeChangesPanel() {
	if !a.changesOpen {
		return
	}
	a.changesOpen = false
	a.diffRequest++
	a.diffLoading = false
	if a.diffCancel != nil {
		a.diffCancel()
		a.diffCancel = nil
	}
}

// changesPanel is the review drawer: the changed files and the selected file's
// diff, rendered by MyGo's diff viewer.
func (a *App) changesPanel(c *ui.Context) {
	t := c.Theme()
	ui.Column(c.Key("git:"+a.status.SessionID)).Fill().MinWidth(0).Background(t.Surface).
		BorderWidth(0, 0, 0, 1).BorderColor(t.Border).Children(func() {
		ui.Row(c).Padding(8, 12).Gap(8).AlignItems(ui.Center).
			BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			ui.Column(c).Grow(1).Gap(1).Children(func() {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, changesMonochromeIcon).Size(15, 15).TextColor(t.Text)
					ui.Text(c, "Git").FontSize(13).Bold()
				})
				if a.changes.RepoRoot != "" {
					repo := filepath.Base(a.changes.RepoRoot)
					if a.changes.Branch != "" {
						repo += "  ·  " + a.changes.Branch
					}
					ui.Text(c, repo).FontSize(10.5).TextColor(t.TextMuted).Tooltip(a.changes.RepoRoot)
				}
			})
			ui.Text(c, fmt.Sprintf("+%d −%d", a.changes.Added, a.changes.Deleted)).
				FontSize(11).TextColor(t.TextMuted)
			if subtleButton(c, "↻").Label("Refresh Git").Tooltip("Refresh Git").Clicked() {
				a.refreshChanges()
			}
			if subtleButton(c, "×").Label("Close Git").Tooltip("Close Git").Clicked() {
				a.closeChangesPanel()
			}
		})
		if a.changes.NotGitRepo {
			ui.Column(c).Grow(1).Center().Padding(24).Children(func() {
				ui.Text(c, "This project is not a Git repository.").FontSize(12).TextColor(t.TextMuted)
			})
			return
		}
		state := a.gitState(a.status.SessionID)
		ui.Row(c).Padding(6, 10).Gap(6).Wrap().Children(func() {
			for _, action := range []struct{ label, name string }{{"Fetch", "fetch"}, {"Pull", "pull"}, {"Push", "push"}} {
				if subtleButton(c, action.label, a.gitDisabled()).Clicked() {
					a.runGitAction(action.name, "")
				}
			}
			if a.changes.Upstream != "" {
				ui.Text(c, fmt.Sprintf("%s · ↑%d ↓%d", a.changes.Upstream, a.changes.Ahead, a.changes.Behind)).FontSize(10.5).TextColor(t.TextMuted).Wrap()
			} else {
				ui.Text(c, "No upstream configured").FontSize(10.5).TextColor(t.TextMuted)
			}
		})
		ui.Row(c).Padding(6, 10).Gap(6).Children(func() {
			ui.TextInput(c, &state.commitMessage).Label("Commit message").Placeholder("Commit message (staged changes only)").Grow(1).Disabled(a.gitDisabled())
			if subtleButton(c, "Commit", a.gitDisabled() || a.changes.StagedFiles == 0 || strings.TrimSpace(state.commitMessage) == "").Clicked() {
				a.runGitAction("commit", "")
			}
		})
		if state.busy {
			ui.Text(c, "Git "+state.operation+"…").Padding(4, 10).FontSize(11).TextColor(t.TextMuted)
		}
		if state.err != "" {
			ui.Text(c, state.err).Padding(4, 10).FontSize(11).TextColor(t.Danger).Selectable().Tooltip(state.err).Wrap().MaxLines(6)
		}
		if state.result != "" {
			ui.Text(c, state.result).Padding(4, 10).FontSize(11).TextColor(t.TextMuted).Selectable().Tooltip(state.result).Wrap().MaxLines(4)
		}
		ui.Row(c).Padding(7, 10).Gap(6).AlignItems(ui.Center).
			BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			for _, option := range []struct {
				label string
				view  appsession.ChangeView
			}{{"Unstaged", appsession.ChangesUnstaged}, {"Staged", appsession.ChangesStaged}} {
				option := option
				button := ui.ButtonBase(c.Key(string(option.view))).Padding(5, 9).Radius(6).Label(option.label)
				hoverBorder(c, button)
				if a.currentChangesView() == option.view {
					button.Background(t.SurfacePressed)
				} else if button.Hovered() {
					button.Background(t.SurfaceHover)
				}
				button.Children(func() { ui.Text(c, option.label).FontSize(11.5) })
				if button.Clicked() && a.currentChangesView() != option.view {
					a.setChangesView(option.view)
				}
			}
			ui.Spacer(c)
			action := "Stage all"
			stage := true
			if a.currentChangesView() == appsession.ChangesStaged {
				action, stage = "Unstage all", false
			}
			if subtleButton(c, action, len(a.changes.Files) == 0 || a.gitDisabled()).Clicked() {
				a.setAllStaged(stage)
			}
		})
		if a.changes.Truncated {
			ui.Text(c, "The changed-file list is limited; narrow generated output outside the app.").
				FontSize(11).TextColor(t.Danger).Padding(6, 12)
		}
		ui.Scroll(c).MaxHeight(190).Children(func() {
			ui.Column(c).Padding(6).Gap(1).Children(func() {
				if len(a.changes.Files) == 0 {
					empty := "No unstaged changes."
					if a.currentChangesView() == appsession.ChangesStaged {
						empty = "No staged changes."
					}
					ui.Text(c, empty).FontSize(12).TextColor(t.TextMuted).Padding(8)
				}
				for i, f := range a.changes.Files {
					i, f := i, f
					row := ui.Row(c.Key(f.Path)).Padding(5, 8).Gap(8).Radius(5).Cursor(ui.CursorPointer)
					if i == a.diffSel {
						row.Background(t.SurfacePressed)
					} else if row.Hovered() {
						row.Background(t.SurfaceHover)
					}
					row.Children(func() {
						ui.Text(c, changeStatusIcon(f.Status)).FontSize(12).TextColor(t.TextMuted).Width(18).Tooltip(f.Status)
						ui.Text(c, f.Path).FontSize(12).SingleLine().Ellipsis("…").Grow(1)
						if f.Added > 0 {
							ui.Text(c, fmt.Sprintf("+%d", f.Added)).FontSize(10.5).TextColor(t.Accent)
						}
						if f.Deleted > 0 {
							ui.Text(c, fmt.Sprintf("−%d", f.Deleted)).FontSize(10.5).TextColor(t.Danger)
						}
						action := "Stage"
						stage := true
						if a.currentChangesView() == appsession.ChangesStaged {
							action, stage = "Unstage", false
						}
						if subtleButton(c, action, a.gitDisabled()).Clicked() {
							a.setFileStaged(f.Path, stage)
						}
					})
					if row.Clicked() {
						a.selectDiffRow(i)
					}
				}
			})
		})
		ui.Box(c).Height(1).Background(t.Border)
		if a.fileDiff.Truncated {
			ui.Text(c, "This file diff is large; only the first 2 MiB is loaded.").
				FontSize(11).TextColor(t.Danger).Padding(6, 12)
		}
		if a.diffLoading {
			ui.Column(c).Grow(1).Center().Children(func() {
				ui.Text(c, "Loading selected file diff…").FontSize(12).TextColor(t.TextMuted)
			})
		} else if a.diff != nil {
			ui.Column(c).Grow(1).MinHeight(0).Children(func() {
				editor.DiffView(c, a.diff).Grow(1).FillWidth()
			})
		} else {
			ui.Column(c).Grow(1).Center().Children(func() {
				message := "Select a file to review its diff."
				if len(a.changes.Files) > 0 {
					message = "This file has no textual diff."
				}
				ui.Text(c, message).FontSize(12).TextColor(t.TextMuted)
			})
		}
	})
}

func (a *App) currentChangesView() appsession.ChangeView {
	if a.changesView == appsession.ChangesStaged {
		return appsession.ChangesStaged
	}
	return appsession.ChangesUnstaged
}

func (a *App) setChangesView(view appsession.ChangeView) {
	a.changesView = view
	a.diffSel = 0
	a.refreshChanges()
}

func (a *App) setAllStaged(staged bool) {
	action := "unstage"
	if staged {
		action = "stage"
	}
	a.runGitAction(action, "")
}

func (a *App) setFileStaged(path string, staged bool) {
	action := "unstage"
	if staged {
		action = "stage"
	}
	a.runGitAction(action, path)
}

func changeStatusIcon(status string) string {
	switch status {
	case "A", "?":
		return "+"
	case "D":
		return "−"
	case "R", "C":
		return "→"
	default:
		return "●"
	}
}
