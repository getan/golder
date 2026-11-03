package mygoui

import (
	"bytes"
	"context"
	"fmt"
	"image"
	_ "image/png"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	"github.com/getan/golder/gui/appsession"
)

// openWorkspaceTerminal opens the existing workspace in a MyGo terminal
// window. The workspace daemon, PTYs and VT stay tuios-mini's: this window only
// runs the same `golder workspace attach` a person would run in a terminal.
func (a *App) openWorkspaceTerminal(session string) {
	bin, err := golderBinary()
	if err != nil {
		a.banner, a.bannerErr = err.Error(), true
		return
	}
	term, err := terminal.New(terminal.Options{
		Command: []string{bin, "workspace", "attach", session},
		Dir:     a.project,
	})
	if err != nil {
		a.banner, a.bannerErr = "Terminal unavailable: "+err.Error(), true
		return
	}
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:  "Workspace · " + session,
		Width:  1000,
		Height: 640,
		Content: ui.View(func(c *ui.Context) {
			terminal.View(c, term).Fill().AutoFocus()
		}),
	})
	win.OnClosed(func() {
		// Detaching leaves the daemon and its panes running, exactly as
		// Ctrl+b d does in the terminal client.
		term.Close()
	})
}

// openBrowserPreview shows a live PNG of a browser pane, refreshed on demand.
func (a *App) openBrowserPreview(b appsession.BrowserPreview) {
	type shot struct {
		bmp *ui.Bitmap
		err string
		at  time.Time
	}
	state := &shot{}
	load := func() {
		go func() {
			ctx := contextWithTimeout(8 * time.Second)
			defer ctx.cancel()
			raw, err := a.ctrl.BrowserScreenshot(ctx.ctx, b.Socket, false)
			var next shot
			if err != nil {
				next.err = err.Error()
			} else if img, _, derr := image.Decode(bytes.NewReader(raw)); derr != nil {
				next.err = derr.Error()
			} else {
				next.bmp = ui.NewBitmap(img)
			}
			next.at = time.Now()
			win := a.win
			if win != nil {
				win.Update(func() { *state = next })
			}
		}()
	}
	title := b.Title
	if title == "" {
		title = b.URL
	}
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:  "Browser · " + title,
		Width:  1000,
		Height: 720,
		Content: ui.View(func(c *ui.Context) {
			t := c.Theme()
			ui.Column(c).Fill().Gap(8).Padding(10).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, title).FontSize(13).Bold().SingleLine().Ellipsis("…").Grow(1)
					if ui.Button(c, "Refresh").Clicked() {
						load()
					}
				})
				switch {
				case state.err != "":
					ui.Text(c, state.err).FontSize(12).TextColor(t.Danger)
				case state.bmp == nil:
					ui.Text(c, "Loading…").FontSize(12).TextColor(t.TextMuted)
				default:
					ui.Column(c).Grow(1).Center().Children(func() {
						ui.Image(c, state.bmp).MaxWidth(940)
					})
				}
				ui.Text(c, "Read-only preview from the browser control socket; use the browser tool to interact.").
					FontSize(11).TextColor(t.TextMuted)
			})
		}),
	})
	win.OnClosed(func() {})
	load()
}

type timeoutCtx struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func contextWithTimeout(d time.Duration) timeoutCtx {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	return timeoutCtx{ctx: ctx, cancel: cancel}
}

// golderBinary finds the CLI binary the workspace window runs.
func golderBinary() (string, error) {
	if v := os.Getenv("GOLDER_BIN"); v != "" {
		return v, nil
	}
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "golder")
		if runtimeIsWindows() {
			candidate += ".exe"
		}
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate, nil
		}
	}
	if p, err := exec.LookPath("golder"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("cannot find the golder binary; set GOLDER_BIN to open a terminal workspace")
}

func runtimeIsWindows() bool {
	return os.PathSeparator == '\\'
}

func init() {
	log.SetPrefix("[golder-gui] ")
}
