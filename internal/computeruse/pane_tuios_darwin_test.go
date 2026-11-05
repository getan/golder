//go:build darwin

package computeruse

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getan/golder/internal/tuiosctl"
	"github.com/getan/tuios-mini/mini"
)

// A test binary must dispatch the same internal helper entry point as golder so
// the opt-in pane integration test can launch itself in a real tuios pane.
func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == "computer-pane" {
		if err := RunOperatorPane(context.Background(), os.Args[2], os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type paneLiveFixture struct {
	png   []byte
	click chan struct{}
	fill  chan string
}

func (*paneLiveFixture) Validate(context.Context, Target, bool) error { return nil }
func (*paneLiveFixture) Close() error                                 { return nil }
func (f *paneLiveFixture) Capture(context.Context, Target) ([]byte, error) {
	return f.png, nil
}
func (*paneLiveFixture) Snapshot(context.Context, Target) (string, []Element, bool, error) {
	return "fixture-snapshot", []Element{
		{Ref: "@e1", Role: "button", Name: "PaneTestButton", Enabled: true},
		{Ref: "@e2", Role: "text", Name: "PaneTextBox", Enabled: true, Editable: true},
	}, false, nil
}
func (*paneLiveFixture) SupportsAction(action string) bool {
	return action == "click" || action == "fill"
}
func (f *paneLiveFixture) Act(_ context.Context, _ Target, id, ref, action, text string) error {
	if id != "fixture-snapshot" {
		return ErrStaleObservation
	}
	switch {
	case ref == "@e1" && action == "click":
		f.click <- struct{}{}
	case ref == "@e2" && action == "fill":
		f.fill <- text
	default:
		return ErrStaleObservation
	}
	return nil
}

// This exercises the real tuios pane, authenticated IPC, viewer and teardown
// with a synthetic frame; it does not observe or operate any desktop app.
func TestPaneLaunchTuiosLive(t *testing.T) {
	if os.Getenv("GOLDER_COMPUTER_PANE_TEST") != "1" {
		t.Skip("set GOLDER_COMPUTER_PANE_TEST=1 inside a local tuios pane")
	}
	if !tuiosctl.Available() {
		t.Fatal("test must run inside a local tuios pane")
	}
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture := &paneLiveFixture{png: imageData.Bytes(), click: make(chan struct{}, 1), fill: make(chan string, 1)}
	s := New(fixture)
	if err := s.Start(ctx, Target{ID: "fixture", Application: "Fixture", Window: "Preview"}, Capabilities{Observe: true, Control: true}); err != nil {
		t.Fatal(err)
	}
	handle, err := LaunchOperatorPane(ctx, s, ".")
	if err != nil {
		_ = s.Close()
		t.Fatal(err)
	}
	defer handle.Close()
	client, err := tuiosctl.Dial(ctx, tuiosctl.FromEnv())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	result, err := client.WaitFor(ctx, tuiosctl.WaitParams{Condition: "window-output", Window: handle.WindowID(), Pattern: "Computer Use", Timeout: 10 * time.Second})
	if err != nil || !result.Matched {
		t.Fatalf("viewer did not display in its pane: result=%+v err=%v", result, err)
	}
	// This isolated synthetic pane is driven as a person for the fixture only;
	// production pane tools refuse agent input into operator panes.
	if _, err := client.SendText(ctx, handle.WindowID(), "observe", true); err != nil {
		t.Fatal(err)
	}
	result, err = client.WaitFor(ctx, tuiosctl.WaitParams{Condition: "window-output", Window: handle.WindowID(), Pattern: "PaneTestButton", Timeout: 10 * time.Second})
	if err != nil || !result.Matched {
		t.Fatalf("semantic controls not displayed: result=%+v err=%v", result, err)
	}
	if _, err := client.SendText(ctx, handle.WindowID(), "click @e1", true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fixture.click:
	case <-ctx.Done():
		t.Fatal("fixture button was not clicked")
	}
	result, err = client.WaitFor(ctx, tuiosctl.WaitParams{Condition: "window-output", Window: handle.WindowID(), Pattern: "Clicked", Timeout: 10 * time.Second})
	if err != nil || !result.Matched {
		t.Fatalf("click acknowledgement missing: result=%+v err=%v", result, err)
	}
	if _, err := client.SendText(ctx, handle.WindowID(), "observe", true); err != nil {
		t.Fatal(err)
	}
	result, err = client.WaitFor(ctx, tuiosctl.WaitParams{Condition: "window-output", Window: handle.WindowID(), Pattern: "PaneTextBox", Timeout: 10 * time.Second})
	if err != nil || !result.Matched {
		t.Fatalf("editable control not displayed: result=%+v err=%v", result, err)
	}
	if _, err := client.SendText(ctx, handle.WindowID(), "fill @e2 世界  two", true); err != nil {
		t.Fatal(err)
	}
	select {
	case value := <-fixture.fill:
		if value != "世界  two" {
			t.Fatalf("fill value = %q", value)
		}
	case <-ctx.Done():
		t.Fatal("fixture text was not filled")
	}
	if err := client.SendKeys(ctx, handle.WindowID(), "ctrl+g", 1); err != nil {
		t.Fatal(err)
	}
	result, err = client.WaitFor(ctx, tuiosctl.WaitParams{Condition: "window-output", Window: handle.WindowID(), Pattern: "PAUSED", Timeout: 10 * time.Second})
	if err != nil || !result.Matched || !s.Status().Paused {
		t.Fatalf("operator pause failed: result=%+v status=%+v err=%v", result, s.Status(), err)
	}
	handle.Close()
	select {
	case <-handle.Done():
	case <-ctx.Done():
		t.Fatal("pane teardown timed out")
	}
	if s.Status().Active {
		t.Fatal("pane shutdown did not revoke the target")
	}
}

// Opt-in because it allocates a real PTY. The daemon and fixture live in a
// private temporary directory and do not touch the operator's workspace.
func TestPaneEmbeddedTuios(t *testing.T) {
	if os.Getenv("GOLDER_COMPUTER_EMBEDDED_TEST") != "1" {
		t.Skip("set GOLDER_COMPUTER_EMBEDDED_TEST=1 for the embedded tuios test")
	}
	dir, err := os.MkdirTemp("", "cu-embedded-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	srv, err := mini.Start(ctx, mini.Config{
		SocketPath: filepath.Join(dir, "s"), StateDir: filepath.Join(dir, "state"), Session: "computer-test",
		Command: []string{executable, "-test.v", "-test.run=^TestPaneLaunchTuiosLive$"},
		PaneEnv: []string{"GOLDER_COMPUTER_PANE_TEST=1"}, Width: 100, Height: 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	client, err := tuiosctl.Dial(ctx, tuiosctl.Options{Socket: srv.Socket(), Session: srv.Session()})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	windows, err := client.ListWindows(ctx)
	if err != nil || len(windows.Windows) != 1 {
		t.Fatalf("initial pane: %+v, %v", windows, err)
	}
	window := windows.Windows[0].WindowID
	result, err := client.WaitFor(ctx, tuiosctl.WaitParams{Condition: "window-exit", Window: window, Timeout: 35 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	screen, captureErr := client.CapturePane(ctx, window, "recent", 100, false)
	if !result.Matched || result.ExitCode == nil || *result.ExitCode != 0 || captureErr != nil || !strings.Contains(screen.Content, "PASS") {
		t.Fatalf("pane integration failed: result=%+v capture=%v output=%q", result, captureErr, screen.Content)
	}
}
