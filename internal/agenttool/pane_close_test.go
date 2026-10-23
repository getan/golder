package agenttool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/tuiosctl"
)

type paneCloseFake struct {
	calls    []string
	windows  []tuiosctl.Window
	listErr  error
	sendErr  error
	waitErr  error
	closeErr error
	result   tuiosctl.WaitResult
	onWait   func(context.Context)
}

func (f *paneCloseFake) PaneID() string { return "own" }

func (f *paneCloseFake) ListWindows(context.Context) (tuiosctl.WindowList, error) {
	f.calls = append(f.calls, "list")
	return tuiosctl.WindowList{Windows: f.windows}, f.listErr
}

func (f *paneCloseFake) SendKeys(_ context.Context, window, keys string, repeat int) error {
	f.calls = append(f.calls, fmt.Sprintf("keys:%s:%s:%d", window, keys, repeat))
	return f.sendErr
}

func (f *paneCloseFake) WaitFor(ctx context.Context, p tuiosctl.WaitParams) (tuiosctl.WaitResult, error) {
	f.calls = append(f.calls, "wait:"+p.Window+":"+p.Condition)
	if f.onWait != nil {
		f.onWait(ctx)
	}
	return f.result, f.waitErr
}

func (f *paneCloseFake) CloseWindow(_ context.Context, window string) error {
	f.calls = append(f.calls, "close:"+window)
	return f.closeErr
}

func browserCloseWindow() tuiosctl.Window {
	return tuiosctl.Window{WindowID: "browser-id", ForegroundCmd: "kitty-browse"}
}

func TestClosePaneWaitsForBrowserBeforeHardClose(t *testing.T) {
	win := browserCloseWindow()
	zero := 0
	f := &paneCloseFake{windows: []tuiosctl.Window{win}, result: tuiosctl.WaitResult{Matched: true, ExitCode: &zero}}
	out, err := closePane(context.Background(), f, win, false, false, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"keys:browser-id:Escape:6", "keys:browser-id:ctrl+q:0", "wait:browser-id:window-exit", "list", "close:browser-id"}
	if !slices.Equal(f.calls, want) || out["close_mode"] != "browser-exit" || out["exit_code"] != 0 {
		t.Fatalf("calls=%v out=%v", f.calls, out)
	}
}

func TestClosePaneAutoRemovedBrowserNeedsNoHardClose(t *testing.T) {
	f := &paneCloseFake{result: tuiosctl.WaitResult{Matched: true}}
	out, err := closePane(context.Background(), f, browserCloseWindow(), false, false, time.Second)
	if err != nil || out["close_mode"] != "browser-exit" || slices.Contains(f.calls, "close:browser-id") {
		t.Fatalf("calls=%v out=%v err=%v", f.calls, out, err)
	}
}

func TestClosePaneBrowserFailuresNeedExplicitForce(t *testing.T) {
	for _, failure := range []string{"timeout", "send", "unmatched"} {
		for _, force := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/force=%v", failure, force), func(t *testing.T) {
				f := &paneCloseFake{}
				switch failure {
				case "timeout":
					f.waitErr = &tuiosctl.Error{Code: "timeout", Message: "still running"}
				case "send":
					f.sendErr = errors.New("cannot send quit")
				}
				out, err := closePane(context.Background(), f, browserCloseWindow(), false, force, time.Second)
				if force {
					if err != nil || out["close_mode"] != "forced" || out["warning"] == "" || !slices.Contains(f.calls, "close:browser-id") {
						t.Fatalf("calls=%v out=%v err=%v", f.calls, out, err)
					}
				} else if err == nil || !strings.Contains(err.Error(), "no hard close") || slices.Contains(f.calls, "close:browser-id") {
					t.Fatalf("calls=%v out=%v err=%v", f.calls, out, err)
				}
			})
		}
	}
}

func TestClosePaneCancellationNeverForces(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := &paneCloseFake{onWait: func(context.Context) { cancel() }, waitErr: context.Canceled}
	_, err := closePane(ctx, f, browserCloseWindow(), false, true, time.Second)
	if !errors.Is(err, context.Canceled) || slices.Contains(f.calls, "close:browser-id") {
		t.Fatalf("calls=%v err=%v", f.calls, err)
	}
}

func TestClosePaneRealDeadlineIsBounded(t *testing.T) {
	f := &paneCloseFake{onWait: func(ctx context.Context) { <-ctx.Done() }, waitErr: context.DeadlineExceeded}
	start := time.Now()
	_, err := closePane(context.Background(), f, browserCloseWindow(), false, false, 100*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second || slices.Contains(f.calls, "close:browser-id") {
		t.Fatalf("calls=%v elapsed=%s err=%v", f.calls, time.Since(start), err)
	}
}

func TestClosePaneVerifiesAlreadyGoneInsteadOfGuessing(t *testing.T) {
	for _, present := range []bool{false, true} {
		f := &paneCloseFake{waitErr: &tuiosctl.Error{Code: "window_not_found"}}
		if present {
			f.windows = []tuiosctl.Window{browserCloseWindow()}
		}
		out, err := closePane(context.Background(), f, browserCloseWindow(), false, false, time.Second)
		if present && err == nil || !present && (err != nil || out["close_mode"] != "already-closed") {
			t.Fatalf("present=%v out=%v err=%v", present, out, err)
		}
		if slices.Contains(f.calls, "close:browser-id") {
			t.Fatal("hard close sent after window_not_found")
		}
	}
}

func TestClosePaneListingFailureIsNotExit(t *testing.T) {
	f := &paneCloseFake{result: tuiosctl.WaitResult{Matched: true}, listErr: errors.New("daemon unavailable")}
	_, err := closePane(context.Background(), f, browserCloseWindow(), false, true, time.Second)
	if err == nil || slices.Contains(f.calls, "close:browser-id") {
		t.Fatalf("calls=%v err=%v", f.calls, err)
	}
}

func TestClosePaneDoesNotGuessBrowserFromTitle(t *testing.T) {
	f := &paneCloseFake{}
	win := tuiosctl.Window{WindowID: "terminal", DisplayName: "kitty-browse", Title: "browser"}
	out, err := closePane(context.Background(), f, win, false, false, time.Second)
	if err != nil || out["close_mode"] != "pane" || !slices.Equal(f.calls, []string{"close:terminal"}) {
		t.Fatalf("calls=%v out=%v err=%v", f.calls, out, err)
	}
}

func TestClosePaneWorkspaceHintDoesNotOverrideChangedProgram(t *testing.T) {
	for _, force := range []bool{false, true} {
		f := &paneCloseFake{}
		win := tuiosctl.Window{WindowID: "changed", ForegroundCmd: "vim"}
		out, err := closePane(context.Background(), f, win, true, force, time.Second)
		if force {
			if err != nil || out["close_mode"] != "forced" || !slices.Equal(f.calls, []string{"close:changed"}) {
				t.Fatalf("calls=%v out=%v err=%v", f.calls, out, err)
			}
		} else if err == nil || len(f.calls) != 0 {
			t.Fatalf("calls=%v err=%v", f.calls, err)
		}
	}
}

func TestClosePaneOwnWindowNeverGetsQuitOrClose(t *testing.T) {
	f := &paneCloseFake{}
	win := tuiosctl.Window{WindowID: "own", ForegroundCmd: "kitty-browse"}
	_, err := closePane(context.Background(), f, win, true, true, time.Second)
	if err == nil || len(f.calls) != 0 {
		t.Fatalf("calls=%v err=%v", f.calls, err)
	}
}

func TestBrowserCloseBothToolEntrypoints(t *testing.T) {
	for _, workspace := range []bool{false, true} {
		for _, force := range []bool{false, true} {
			for _, timeout := range []bool{false, true} {
				t.Run(fmt.Sprintf("workspace=%v/force=%v/timeout=%v", workspace, force, timeout), func(t *testing.T) {
					f, sock := startTuiosFake(t)
					useTuiosFake(t, sock)
					f.answer = func(verb string, p map[string]any) any {
						switch verb {
						case "list-windows":
							foreground := "kitty-browse"
							if workspace {
								foreground = "" // workspace records the browser kind
							}
							return map[string]any{"windows": []map[string]any{{"window_id": "w2", "foreground_cmd": foreground}}}
						case "send-keys":
							if p["window"] != "w2" {
								t.Errorf("quit targeted %v, not w2", p["window"])
							}
						case "wait-for":
							if p["condition"] != "window-exit" || p["window"] != "w2" || p["timeout"] != float64(500) {
								t.Errorf("unexpected wait params: %v", p)
							}
							if timeout {
								return &tuiosctl.Error{Code: "timeout", Message: "not exited"}
							}
							return map[string]any{"matched": true, "condition": "window-exit", "exit_code": 0}
						}
						return map[string]any{"type": "ok"}
					}
					var tool agentcore.AgentTool
					var wt *WorkspaceTool
					args := map[string]any{"action": "close", "force": force, "timeout_ms": 500}
					if workspace {
						wt = NewWorkspaceTool(t.TempDir(), true)
						wt.panes["browser"] = workspacePane{Name: "browser", Kind: "browser", WindowID: "w2"}
						tool, args["name"] = wt, "browser"
					} else {
						tool, args["window"] = newTuiosTool(t, true), "w2"
					}
					raw, _ := json.Marshal(args)
					res, err := tool.Execute(context.Background(), "close", raw, nil)
					if err != nil {
						t.Fatal(err)
					}
					failed := timeout && !force
					if res.IsError != failed || f.seen("close-window") == failed {
						t.Fatalf("res=%s close-window=%v", agentcore.ContentToText(res.Content), f.seen("close-window"))
					}
					if wt != nil {
						_, retained := wt.panes["browser"]
						if retained != failed {
							t.Fatalf("workspace retained=%v, want %v", retained, failed)
						}
					}
				})
			}
		}
	}
}
