package agenttool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getan/golder/internal/agentcore"
	"github.com/getan/golder/internal/browserctl"
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

// controlFake is a browser control endpoint on a real Unix socket, so the close
// path exercises the same permission check, HTTP envelope and capability probe
// it uses against a live browser.
type controlFake struct {
	path string

	mu       sync.Mutex
	id       string
	caps     []string
	quits    int
	infoHits int
	failQuit bool
	quitBody string // overrides the standard success envelope
}

func newControlFake(t *testing.T, path, id string, caps ...string) *controlFake {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	f := &controlFake{path: path, id: id, caps: caps}
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Skipf("Unix socket unavailable: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: f}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return f
}

func (f *controlFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	id, caps, failQuit, quitBody := f.id, f.caps, f.failQuit, f.quitBody
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/info":
		f.mu.Lock()
		f.infoHits++
		f.mu.Unlock()
		out := make([]browserctl.Capability, 0, len(caps))
		for _, name := range caps {
			out = append(out, browserctl.Capability{Name: name})
		}
		_ = json.NewEncoder(w).Encode(browserctl.Info{
			APIVersion: "v1", Backend: "native-cdp", BrowserID: id, Capabilities: out,
		})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/quit":
		f.mu.Lock()
		f.quits++
		f.mu.Unlock()
		if failQuit {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"apiVersion":"v1","id":null,"ok":false,"error":{"code":"internal_error","message":"cannot quit"}}`)
			return
		}
		if quitBody != "" {
			_, _ = io.WriteString(w, quitBody)
			return
		}
		_, _ = io.WriteString(w, `{"apiVersion":"v1","id":null,"ok":true,"result":{"quitting":true}}`)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"apiVersion":"v1","id":null,"ok":false,"error":{"code":"not_found","message":"no such endpoint"}}`)
	}
}

func (f *controlFake) setID(id string) {
	f.mu.Lock()
	f.id = id
	f.mu.Unlock()
}

func (f *controlFake) counts() (quits, infos int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.quits, f.infoHits
}

// managedPane records a browser pane the way launch does and returns the
// identity close would use for it.
func managedPane(t *testing.T, root, windowID, socket, browserID string, embedded bool) browserPane {
	t.Helper()
	program := "kitty-browse"
	if embedded {
		program = "golder"
	}
	return managedProgramPane(t, root, windowID, socket, program, browserID, embedded)
}

// managedProgramPane is the same record with an explicit launch program name,
// which is what a versioned or renamed install reports.
func managedProgramPane(t *testing.T, root, windowID, socket, program, browserID string, embedded bool) browserPane {
	t.Helper()
	record := browserPaneRecord{
		Root: root, Name: "browser", WindowID: windowID, Socket: socket,
		Program: program, BrowserID: browserID, Embedded: embedded,
	}
	rememberBrowserPane(record)
	t.Cleanup(func() { forgetBrowserPane(root, windowID) })
	return browserPane{
		Root: root, Name: record.Name, Managed: true, Embedded: embedded,
		WindowID: windowID, Socket: socket, Program: program, BrowserID: browserID,
	}
}

func browserCloseWindow() tuiosctl.Window {
	return tuiosctl.Window{WindowID: "browser-id", ForegroundCmd: "kitty-browse"}
}

// golder's own browse reports the process name the requirement pins: golder.
func embeddedBrowserWindow() tuiosctl.Window {
	return tuiosctl.Window{WindowID: "browser-id", ForegroundCmd: "golder"}
}

// A managed embedded pane quits through /v1/quit on the endpoint recorded for
// it, and its exit is confirmed before the pane shell is removed.
func TestClosePaneEmbeddedQuitsRecordedEndpointThenConfirmsExit(t *testing.T) {
	root := shortRoot(t)
	ctl := newControlFake(t, filepath.Join(root, "browser.sock"), "browser-1", "quit", "snapshot")
	pane := managedPane(t, root, "browser-id", ctl.path, "browser-1", true)
	win := embeddedBrowserWindow()
	zero := 0
	f := &paneCloseFake{windows: []tuiosctl.Window{win}, result: tuiosctl.WaitResult{Matched: true, ExitCode: &zero}}
	out, err := closePane(context.Background(), f, win, pane, false, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	quits, _ := ctl.counts()
	want := []string{"wait:browser-id:window-exit", "list", "close:browser-id"}
	if !slices.Equal(f.calls, want) || quits != 1 {
		t.Fatalf("calls=%v quits=%d, want %v and one quit", f.calls, quits, want)
	}
	if out["close_mode"] != "browser-quit" || out["browser_id"] != "browser-1" || out["exit_code"] != 0 {
		t.Fatalf("out=%v", out)
	}
	if _, ok := managedBrowserPane(root, "browser-id"); ok {
		t.Fatal("closed pane kept its record")
	}
}

// The same socket can later serve a different browser. The pane id and the
// foreground program both still look right, so only the recorded browserId can
// tell the difference — and a quit must not be sent to the newcomer.
func TestClosePaneRefusesWhenEndpointServesADifferentBrowser(t *testing.T) {
	root := shortRoot(t)
	ctl := newControlFake(t, filepath.Join(root, "browser.sock"), "browser-1", "quit")
	pane := managedPane(t, root, "browser-id", ctl.path, "browser-1", true)
	ctl.setID("browser-2")
	for _, force := range []bool{false, true} {
		f := &paneCloseFake{result: tuiosctl.WaitResult{Matched: true}}
		out, err := closePane(context.Background(), f, embeddedBrowserWindow(), pane, force, time.Second)
		if force {
			if err != nil || out["close_mode"] != "forced" || !slices.Equal(f.calls, []string{"close:browser-id"}) {
				t.Fatalf("force calls=%v out=%v err=%v", f.calls, out, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), `browser-2`) || !strings.Contains(err.Error(), "no keys and no quit") {
			t.Fatalf("err=%v", err)
		}
		if quits, _ := ctl.counts(); quits != 0 || len(f.calls) != 0 {
			t.Fatalf("quits=%d calls=%v", quits, f.calls)
		}
	}
}

// A pane id that does not match the record is a different pane, whatever the
// foreground program says. Nothing is closed.
func TestClosePaneRefusesAChangedPaneID(t *testing.T) {
	root := shortRoot(t)
	ctl := newControlFake(t, filepath.Join(root, "browser.sock"), "browser-1", "quit")
	pane := managedPane(t, root, "recorded-pane", ctl.path, "browser-1", true)
	win := tuiosctl.Window{WindowID: "browser-id", ForegroundCmd: "golder"}
	f := &paneCloseFake{}
	_, err := closePane(context.Background(), f, win, pane, false, time.Second)
	if err == nil || !strings.Contains(err.Error(), "recorded-pane") || len(f.calls) != 0 {
		t.Fatalf("calls=%v err=%v", f.calls, err)
	}
	if quits, _ := ctl.counts(); quits != 0 {
		t.Fatal("quit sent for a pane golder never started")
	}
}

// "golder" as a foreground program is not evidence by itself: without a record
// there is no way to know which endpoint the pane owns.
func TestClosePaneRefusesUnrecordedGolderPane(t *testing.T) {
	for _, force := range []bool{false, true} {
		f := &paneCloseFake{}
		out, err := closePane(context.Background(), f, embeddedBrowserWindow(), browserPane{}, force, time.Second)
		if force {
			if err != nil || out["close_mode"] != "forced" || !slices.Equal(f.calls, []string{"close:browser-id"}) {
				t.Fatalf("force calls=%v out=%v err=%v", f.calls, out, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), "no keys and no quit") || len(f.calls) != 0 {
			t.Fatalf("calls=%v err=%v", f.calls, err)
		}
	}
}

// A recorded endpoint that no longer answers must not be replaced by a guess:
// the socket may be gone because the browser exited, or because it moved.
func TestClosePaneRefusesUnreachableRecordedEndpoint(t *testing.T) {
	root := shortRoot(t)
	pane := managedPane(t, root, "browser-id", filepath.Join(root, "gone.sock"), "browser-1", true)
	for _, force := range []bool{false, true} {
		f := &paneCloseFake{}
		out, err := closePane(context.Background(), f, embeddedBrowserWindow(), pane, force, time.Second)
		if force {
			if err != nil || out["close_mode"] != "forced" {
				t.Fatalf("force out=%v err=%v", out, err)
			}
			continue
		}
		if err == nil || len(f.calls) != 0 {
			t.Fatalf("calls=%v err=%v", f.calls, err)
		}
	}
}

// A verified browser that does not advertise quit cannot be asked to leave, and
// keys are not a substitute for a process golder started.
func TestClosePaneEmbeddedWithoutQuitCapabilityNeverSendsKeys(t *testing.T) {
	root := shortRoot(t)
	ctl := newControlFake(t, filepath.Join(root, "browser.sock"), "browser-1", "snapshot")
	pane := managedPane(t, root, "browser-id", ctl.path, "browser-1", true)
	for _, force := range []bool{false, true} {
		f := &paneCloseFake{}
		out, err := closePane(context.Background(), f, embeddedBrowserWindow(), pane, force, time.Second)
		if force {
			if err != nil || out["close_mode"] != "forced" || !slices.Equal(f.calls, []string{"close:browser-id"}) {
				t.Fatalf("force calls=%v out=%v err=%v", f.calls, out, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), `"quit"`) || len(f.calls) != 0 {
			t.Fatalf("calls=%v err=%v", f.calls, err)
		}
	}
	if quits, _ := ctl.counts(); quits != 0 {
		t.Fatal("quit sent to a browser that does not advertise it")
	}
}

// An external kitty-browse golder started, with no quit capability: the legacy
// key sequence is allowed because identity was proved first.
func TestClosePaneLegacyExternalKittyBrowseUsesKeys(t *testing.T) {
	root := shortRoot(t)
	ctl := newControlFake(t, filepath.Join(root, "browser.sock"), "browser-1", "snapshot")
	pane := managedPane(t, root, "browser-id", ctl.path, "browser-1", false)
	win := browserCloseWindow()
	zero := 0
	f := &paneCloseFake{windows: []tuiosctl.Window{win}, result: tuiosctl.WaitResult{Matched: true, ExitCode: &zero}}
	out, err := closePane(context.Background(), f, win, pane, false, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"keys:browser-id:Escape:6", "keys:browser-id:ctrl+q:0", "wait:browser-id:window-exit", "list", "close:browser-id"}
	if !slices.Equal(f.calls, want) || out["close_mode"] != "browser-exit" || out["exit_code"] != 0 {
		t.Fatalf("calls=%v out=%v", f.calls, out)
	}
	if quits, _ := ctl.counts(); quits != 0 {
		t.Fatal("quit sent to an endpoint that does not advertise it")
	}
}

// A newer external kitty-browse advertises quit, so it is closed through the
// endpoint and no keys are typed into its pane.
func TestClosePaneExternalWithQuitCapabilityUsesQuit(t *testing.T) {
	root := shortRoot(t)
	ctl := newControlFake(t, filepath.Join(root, "browser.sock"), "browser-1", "quit")
	pane := managedPane(t, root, "browser-id", ctl.path, "browser-1", false)
	f := &paneCloseFake{result: tuiosctl.WaitResult{Matched: true}}
	out, err := closePane(context.Background(), f, browserCloseWindow(), pane, false, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.calls, []string{"wait:browser-id:window-exit", "list"}) || out["close_mode"] != "browser-quit" {
		t.Fatalf("calls=%v out=%v", f.calls, out)
	}
	if quits, _ := ctl.counts(); quits != 1 {
		t.Fatalf("quits=%d, want 1", quits)
	}
}

// A kitty-browse pane golder did not start cannot be identified, so neither
// keys nor a quit are sent without force.
func TestClosePaneUnrecordedKittyBrowseRefuses(t *testing.T) {
	for _, force := range []bool{false, true} {
		f := &paneCloseFake{}
		out, err := closePane(context.Background(), f, browserCloseWindow(), browserPane{}, force, time.Second)
		if force {
			if err != nil || out["close_mode"] != "forced" || !slices.Equal(f.calls, []string{"close:browser-id"}) {
				t.Fatalf("force calls=%v out=%v err=%v", f.calls, out, err)
			}
			continue
		}
		if err == nil || len(f.calls) != 0 {
			t.Fatalf("calls=%v err=%v", f.calls, err)
		}
	}
}

func TestClosePaneCancelBeforeStartSendsNothing(t *testing.T) {
	root := shortRoot(t)
	ctl := newControlFake(t, filepath.Join(root, "browser.sock"), "browser-1", "quit")
	pane := managedPane(t, root, "browser-id", ctl.path, "browser-1", true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &paneCloseFake{}
	_, err := closePane(ctx, f, embeddedBrowserWindow(), pane, false, time.Second)
	if !errors.Is(err, context.Canceled) || len(f.calls) != 0 {
		t.Fatalf("calls=%v err=%v", f.calls, err)
	}
	if quits, infos := ctl.counts(); quits != 0 || infos != 0 {
		t.Fatalf("quits=%d infos=%d after cancellation", quits, infos)
	}
}

// Cancellation during the exit wait is never permission to hard-kill, even with
// force set.
func TestClosePaneCancellationDuringWaitNeverForces(t *testing.T) {
	root := shortRoot(t)
	ctl := newControlFake(t, filepath.Join(root, "browser.sock"), "browser-1", "quit")
	pane := managedPane(t, root, "browser-id", ctl.path, "browser-1", true)
	ctx, cancel := context.WithCancel(context.Background())
	f := &paneCloseFake{onWait: func(context.Context) { cancel() }, waitErr: context.Canceled}
	_, err := closePane(ctx, f, embeddedBrowserWindow(), pane, true, time.Second)
	if !errors.Is(err, context.Canceled) || slices.Contains(f.calls, "close:browser-id") {
		t.Fatalf("calls=%v err=%v", f.calls, err)
	}
}

func TestClosePaneRealDeadlineIsBounded(t *testing.T) {
	root := shortRoot(t)
	ctl := newControlFake(t, filepath.Join(root, "browser.sock"), "browser-1", "quit")
	pane := managedPane(t, root, "browser-id", ctl.path, "browser-1", true)
	f := &paneCloseFake{onWait: func(ctx context.Context) { <-ctx.Done() }, waitErr: context.DeadlineExceeded}
	start := time.Now()
	_, err := closePane(context.Background(), f, embeddedBrowserWindow(), pane, false, 100*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second || slices.Contains(f.calls, "close:browser-id") {
		t.Fatalf("calls=%v elapsed=%s err=%v", f.calls, time.Since(start), err)
	}
}

// A failed quit or an unconfirmed exit needs explicit force, and the error says
// that no hard close was sent.
func TestClosePaneQuitFailuresNeedExplicitForce(t *testing.T) {
	root := shortRoot(t)
	for _, failure := range []string{"quit", "timeout", "unmatched"} {
		for _, force := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/force=%v", failure, force), func(t *testing.T) {
				ctl := newControlFake(t, filepath.Join(root, "browser.sock"), "browser-1", "quit")
				switch failure {
				case "quit":
					ctl.mu.Lock()
					ctl.failQuit = true
					ctl.mu.Unlock()
				case "timeout":
					// the fake pane never exits
				}
				pane := managedPane(t, root, "browser-id", ctl.path, "browser-1", true)
				f := &paneCloseFake{}
				if failure == "timeout" {
					f.waitErr = &tuiosctl.Error{Code: "timeout", Message: "still running"}
				}
				out, err := closePane(context.Background(), f, embeddedBrowserWindow(), pane, force, time.Second)
				if force {
					if err != nil || out["close_mode"] != "forced" || out["warning"] == "" || !slices.Contains(f.calls, "close:browser-id") {
						t.Fatalf("calls=%v out=%v err=%v", f.calls, out, err)
					}
					return
				}
				if err == nil || !strings.Contains(err.Error(), "no hard close") || slices.Contains(f.calls, "close:browser-id") {
					t.Fatalf("calls=%v out=%v err=%v", f.calls, out, err)
				}
			})
		}
	}
}

func TestClosePaneAutoRemovedBrowserNeedsNoHardClose(t *testing.T) {
	root := shortRoot(t)
	ctl := newControlFake(t, filepath.Join(root, "browser.sock"), "browser-1", "quit")
	pane := managedPane(t, root, "browser-id", ctl.path, "browser-1", true)
	f := &paneCloseFake{result: tuiosctl.WaitResult{Matched: true}}
	out, err := closePane(context.Background(), f, embeddedBrowserWindow(), pane, false, time.Second)
	if err != nil || out["close_mode"] != "browser-quit" || slices.Contains(f.calls, "close:browser-id") {
		t.Fatalf("calls=%v out=%v err=%v", f.calls, out, err)
	}
}

func TestClosePaneVerifiesAlreadyGoneInsteadOfGuessing(t *testing.T) {
	root := shortRoot(t)
	for _, present := range []bool{false, true} {
		ctl := newControlFake(t, filepath.Join(root, fmt.Sprintf("browser-%v.sock", present)), "browser-1", "quit")
		pane := managedPane(t, root, "browser-id", ctl.path, "browser-1", true)
		f := &paneCloseFake{waitErr: &tuiosctl.Error{Code: "window_not_found"}}
		if present {
			f.windows = []tuiosctl.Window{embeddedBrowserWindow()}
		}
		out, err := closePane(context.Background(), f, embeddedBrowserWindow(), pane, false, time.Second)
		if present && err == nil || !present && (err != nil || out["close_mode"] != "already-closed") {
			t.Fatalf("present=%v out=%v err=%v", present, out, err)
		}
		if slices.Contains(f.calls, "close:browser-id") {
			t.Fatal("hard close sent after window_not_found")
		}
	}
}

func TestClosePaneListingFailureIsNotExit(t *testing.T) {
	root := shortRoot(t)
	ctl := newControlFake(t, filepath.Join(root, "browser.sock"), "browser-1", "quit")
	pane := managedPane(t, root, "browser-id", ctl.path, "browser-1", true)
	f := &paneCloseFake{result: tuiosctl.WaitResult{Matched: true}, listErr: errors.New("daemon unavailable")}
	_, err := closePane(context.Background(), f, embeddedBrowserWindow(), pane, true, time.Second)
	if err == nil || slices.Contains(f.calls, "close:browser-id") {
		t.Fatalf("calls=%v err=%v", f.calls, err)
	}
}

func TestClosePaneDoesNotGuessBrowserFromTitle(t *testing.T) {
	f := &paneCloseFake{}
	win := tuiosctl.Window{WindowID: "terminal", DisplayName: "kitty-browse", Title: "browser"}
	out, err := closePane(context.Background(), f, win, browserPane{}, false, time.Second)
	if err != nil || out["close_mode"] != "pane" || !slices.Equal(f.calls, []string{"close:terminal"}) {
		t.Fatalf("calls=%v out=%v err=%v", f.calls, out, err)
	}
}

func TestClosePaneManagedPaneThatChangedProgramNeedsForce(t *testing.T) {
	root := shortRoot(t)
	ctl := newControlFake(t, filepath.Join(root, "browser.sock"), "browser-1", "quit")
	pane := managedPane(t, root, "changed", ctl.path, "browser-1", true)
	for _, force := range []bool{false, true} {
		f := &paneCloseFake{}
		win := tuiosctl.Window{WindowID: "changed", ForegroundCmd: "vim"}
		out, err := closePane(context.Background(), f, win, pane, force, time.Second)
		if force {
			if err != nil || out["close_mode"] != "forced" || !slices.Equal(f.calls, []string{"close:changed"}) {
				t.Fatalf("calls=%v out=%v err=%v", f.calls, out, err)
			}
			continue
		}
		if err == nil || len(f.calls) != 0 {
			t.Fatalf("calls=%v err=%v", f.calls, err)
		}
	}
	if quits, _ := ctl.counts(); quits != 0 {
		t.Fatal("quit sent into a pane that no longer runs the browser")
	}
}

// The pane golder itself runs is never closed, even with force and a record
// that points at that exact id.
func TestClosePaneOwnWindowNeverGetsQuitOrClose(t *testing.T) {
	root := shortRoot(t)
	ctl := newControlFake(t, filepath.Join(root, "browser.sock"), "browser-1", "quit")
	pane := managedPane(t, root, "own", ctl.path, "browser-1", true)
	f := &paneCloseFake{}
	win := tuiosctl.Window{WindowID: "own", ForegroundCmd: "golder"}
	_, err := closePane(context.Background(), f, win, pane, true, time.Second)
	if err == nil || len(f.calls) != 0 {
		t.Fatalf("calls=%v err=%v", f.calls, err)
	}
	if quits, _ := ctl.counts(); quits != 0 {
		t.Fatal("quit sent to golder's own pane")
	}
}

// The pane reports the binary's base name, which is not always literally
// "golder" (a versioned install). The recorded program name makes that match,
// and the endpoint is still proved before anything is sent.
func TestClosePaneRecognizesAVersionedOwnBinary(t *testing.T) {
	root := shortRoot(t)
	ctl := newControlFake(t, filepath.Join(root, "browser.sock"), "browser-1", "quit")
	pane := managedProgramPane(t, root, "browser-id", ctl.path, "golder-1.5.0", "browser-1", true)
	f := &paneCloseFake{result: tuiosctl.WaitResult{Matched: true}}
	win := tuiosctl.Window{WindowID: "browser-id", ForegroundCmd: "golder-1.5.0"}
	out, err := closePane(context.Background(), f, win, pane, false, time.Second)
	if err != nil || out["close_mode"] != "browser-quit" {
		t.Fatalf("out=%v err=%v", out, err)
	}
	if quits, _ := ctl.counts(); quits != 1 {
		t.Fatalf("quits=%d, want 1", quits)
	}
}

// Both tool entrypoints go through the same identity check and the same quit.
func TestBrowserCloseBothToolEntrypoints(t *testing.T) {
	for _, workspace := range []bool{false, true} {
		for _, force := range []bool{false, true} {
			for _, failQuit := range []bool{false, true} {
				t.Run(fmt.Sprintf("workspace=%v/force=%v/failQuit=%v", workspace, force, failQuit), func(t *testing.T) {
					root := shortRoot(t)
					f, sock := startTuiosFake(t)
					useTuiosFake(t, sock)
					ctl := newControlFake(t, filepath.Join(root, "browser.sock"), "browser-1", "quit")
					ctl.mu.Lock()
					ctl.failQuit = failQuit
					ctl.mu.Unlock()
					pane := managedPane(t, root, "w2", ctl.path, "browser-1", true)
					f.answer = func(verb string, p map[string]any) any {
						switch verb {
						case "list-windows":
							return map[string]any{"windows": []map[string]any{{"window_id": "w2", "foreground_cmd": "golder"}}}
						case "wait-for":
							if p["condition"] != "window-exit" || p["window"] != "w2" || p["timeout"] != float64(500) {
								t.Errorf("unexpected wait params: %v", p)
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
						wt.panes["browser"] = workspacePane{Name: "browser", Kind: "browser", WindowID: "w2", Socket: pane.Socket}
						tool, args["name"] = wt, "browser"
					} else {
						tool, args["window"] = newTuiosTool(t, true), "w2"
					}
					raw, _ := json.Marshal(args)
					res, err := tool.Execute(context.Background(), "close", raw, nil)
					if err != nil {
						t.Fatal(err)
					}
					failed := failQuit && !force
					if res.IsError != failed || f.seen("close-window") == failed {
						t.Fatalf("res=%s close-window=%v", agentcore.ContentToText(res.Content), f.seen("close-window"))
					}
					if quits, _ := ctl.counts(); quits != 1 {
						t.Fatalf("quits=%d, want exactly one", quits)
					}
					if f.seen("send-keys") {
						t.Fatal("keys were typed into a pane that has a quit endpoint")
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
