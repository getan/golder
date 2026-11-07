package mygoui

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

func (a *App) refreshTTYSessions() {
	a.ttySessions = a.ctrl.TTYSessions()
}

// openUserTerminal starts an explicit user-controlled shell in the project. It
// is not an Agent tool call and is labelled accordingly.
func (a *App) openUserTerminal() {
	a.addTerminal(a.terminalProjectPath(), "", false, nil, "")
}

// openTTYTerminal attaches Ghostty VT rendering to an existing gated bash PTY.
// Closing the window detaches; it does not terminate the Agent command.
func (a *App) openTTYTerminal(id string) {
	conn, err := a.ctrl.AttachTTY(id)
	if err != nil {
		a.banner, a.bannerErr = err.Error(), true
		return
	}
	a.addTerminal(canonicalTerminalProject(a.project), "", false, conn, "Agent TTY · "+id)
}

func (a *App) openTerminalWindow(title string, options terminal.Options) {
	var win *mygo.Window
	baseTitle := title
	options.OnTitle = func(programTitle string) {
		if win == nil || strings.TrimSpace(programTitle) == "" {
			return
		}
		win.Update(func() { win.SetTitle(baseTitle + " · " + programTitle) })
	}
	term, err := terminal.New(options)
	if err != nil {
		if options.Conn != nil {
			_ = options.Conn.Close()
		}
		a.banner, a.bannerErr = "Terminal unavailable: "+err.Error(), true
		return
	}
	win = mygo.NewWindow(mygo.WindowOptions{
		Title:     title,
		Width:     980,
		Height:    620,
		MinWidth:  560,
		MinHeight: 360,
		Content: ui.View(func(c *ui.Context) {
			ui.Column(c).Fill().Children(func() {
				terminal.View(c, term).Fill().AutoFocus()
				ui.Row(c).Height(24).Padding(0, 8).AlignItems(ui.Center).Background(c.Theme().Surface).Children(func() {
					ui.Text(c, terminalFooter(baseTitle)).FontSize(10).TextColor(c.Theme().TextMuted)
				})
			})
		}),
	})
	win.OnClosed(func() { _ = term.Close() })
}

func terminalFooter(title string) string {
	if strings.HasPrefix(title, "Agent Terminal") {
		return "Attached to an approved Agent TTY · closing detaches without killing the command"
	}
	return "User-controlled shell · not an Agent tool call and not covered by Agent sandbox approvals"
}

func toolTTYID(details map[string]any, input map[string]any) string {
	if tty, ok := input["tty"].(bool); !ok || !tty {
		return ""
	}
	value, ok := details["bash_id"]
	if !ok {
		return ""
	}
	switch id := value.(type) {
	case string:
		return strings.TrimSpace(id)
	default:
		return strings.TrimSpace(fmt.Sprint(id))
	}
}
