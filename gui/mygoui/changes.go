package mygoui

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

// changesPanel is the review drawer: the changed files and the selected file's
// diff, rendered by MyGo's diff viewer.
func (a *App) changesPanel(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Width(changesWidth).Shrink(0).MinWidth(320).Background(t.Surface).
		BorderWidth(0, 0, 0, 1).BorderColor(t.Border).Children(func() {
		ui.Row(c).Padding(8, 12).Gap(8).AlignItems(ui.Center).
			BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			ui.Text(c, "Changes").FontSize(13).Bold()
			ui.Text(c, fmt.Sprintf("+%d −%d", a.changes.Added, a.changes.Deleted)).
				FontSize(11).TextColor(t.TextMuted)
			ui.Spacer(c)
			if ui.Button(c, "Refresh").Clicked() {
				a.refreshChanges()
			}
			if ui.Button(c, "Close").Clicked() {
				a.changesOpen = false
			}
		})
		if a.changes.NotGitRepo {
			ui.Column(c).Grow(1).Center().Padding(24).Children(func() {
				ui.Text(c, "This project is not a Git repository.").FontSize(12).TextColor(t.TextMuted)
			})
			return
		}
		if a.changes.Truncated {
			ui.Text(c, "The diff is large; only the beginning is shown.").
				FontSize(11).TextColor(t.Danger).Padding(6, 12)
		}
		ui.Scroll(c).MaxHeight(190).Children(func() {
			ui.Column(c).Padding(6).Gap(1).Children(func() {
				if len(a.changes.Files) == 0 {
					ui.Text(c, "No uncommitted changes.").FontSize(12).TextColor(t.TextMuted).Padding(8)
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
						ui.Text(c, f.Status).FontSize(10.5).TextColor(t.TextMuted).Width(28)
						ui.Text(c, f.Path).FontSize(12).SingleLine().Ellipsis("…").Grow(1)
					})
					if row.Clicked() {
						a.selectDiff(i)
					}
				}
			})
		})
		ui.Box(c).Height(1).Background(t.Border)
		if a.diff != nil {
			ui.Column(c).Grow(1).MinHeight(0).Children(func() {
				editor.DiffView(c, a.diff).Grow(1).FillWidth()
			})
		} else {
			ui.Column(c).Grow(1).Center().Children(func() {
				ui.Text(c, "Select a file to review its diff.").FontSize(12).TextColor(t.TextMuted)
			})
		}
	})
}
