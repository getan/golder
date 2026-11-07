package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/getan/kitty-browse/browserhost"

	"github.com/getan/golder/gui/appsession"
	"github.com/getan/golder/internal/agenttool"
	"github.com/getan/golder/internal/provider"
)

type recordingNativeInput struct{ event browserhost.OperatorInput }

func (r *recordingNativeInput) SendOperatorInput(_ context.Context, event browserhost.OperatorInput) error {
	r.event = event
	return nil
}

func TestNativeOperatorAdapterPreservesExactTargetAndInput(t *testing.T) {
	recorder := &recordingNativeInput{}
	adapter := nativeOperatorAdapter{host: recorder}
	input := appsession.BrowserOperatorInput{BrowserID: "browser", TabID: "tab", TargetID: "target", Kind: "pointer-move", X: 12, Y: 34, DeltaY: 2, Text: "中文", Key: "Enter"}
	if err := adapter.SendOperatorInput(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if recorder.event.BrowserID != input.BrowserID || recorder.event.TabID != input.TabID || recorder.event.TargetID != input.TargetID || recorder.event.Kind != input.Kind || recorder.event.X != input.X || recorder.event.Y != input.Y || recorder.event.DeltaY != input.DeltaY || recorder.event.Text != input.Text || recorder.event.Key != input.Key {
		t.Fatalf("input changed at adapter: %+v", recorder.event)
	}
}

func TestNativeBrowserPassesConfiguredProxy(t *testing.T) {
	h := newNativeBrowserHosts()
	fake := &fakeNativeHost{done: make(chan struct{})}
	var got browserhost.Options
	h.start = func(_ context.Context, opts browserhost.Options) (nativeBrowserHost, error) {
		got = opts
		return fake, nil
	}
	if err := h.Start(context.Background(), agenttool.NativeBrowserRequest{ProxyServer: "http://127.0.0.1:7897"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	if got.ProxyServer != "http://127.0.0.1:7897" || !got.RequireSandbox {
		t.Fatalf("native host options = %+v", got)
	}
}

func TestSavedBrowserProxyReachesNativeHostOptions(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	t.Setenv("GOLDER_PROXY", "")
	settings, err := appsession.LoadProxySettings()
	if err != nil {
		t.Fatal(err)
	}
	settings.URL, settings.Browser = "http://127.0.0.1:7897", true
	if _, err := appsession.SaveProxySettings(settings); err != nil {
		t.Fatal(err)
	}
	route, err := provider.BrowserProxyServer()
	if err != nil {
		t.Fatal(err)
	}
	h := newNativeBrowserHosts()
	fake := &fakeNativeHost{done: make(chan struct{})}
	h.start = func(_ context.Context, opts browserhost.Options) (nativeBrowserHost, error) {
		if opts.ProxyServer != settings.URL || !opts.RequireSandbox {
			t.Errorf("native host received wrong route or sandbox: %+v", opts)
		}
		return fake, nil
	}
	if err := h.Start(context.Background(), agenttool.NativeBrowserRequest{ProxyServer: route}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
}

type fakeNativeHost struct {
	once sync.Once
	done chan struct{}
}

func (f *fakeNativeHost) Wait() error { <-f.done; return nil }
func (f *fakeNativeHost) Close() error {
	f.once.Do(func() { close(f.done) })
	return nil
}

func TestNativeBrowserSurvivesToolCallCancellation(t *testing.T) {
	h := newNativeBrowserHosts()
	fake := &fakeNativeHost{done: make(chan struct{})}
	var lifetime context.Context
	h.start = func(ctx context.Context, opts browserhost.Options) (nativeBrowserHost, error) {
		if !opts.RequireSandbox {
			t.Fatal("Chromium's own sandbox must remain enabled")
		}
		lifetime = ctx
		return fake, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := h.Start(ctx, agenttool.NativeBrowserRequest{}); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-lifetime.Done():
		t.Fatal("browser lifetime followed the completed tool call")
	default:
	}
	h.Close()
	select {
	case <-lifetime.Done():
	case <-time.After(time.Second):
		t.Fatal("GUI shutdown did not release the browser lifetime")
	}
}

func TestNativeBrowserStartupHonorsCancellation(t *testing.T) {
	h := newNativeBrowserHosts()
	started := make(chan struct{})
	h.start = func(ctx context.Context, _ browserhost.Options) (nativeBrowserHost, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Start(ctx, agenttool.NativeBrowserRequest{}) }()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("startup cancellation returned %v", err)
	}
	h.Close()
}

func TestNativeBrowserShutdownCancelsPendingStartup(t *testing.T) {
	h := newNativeBrowserHosts()
	started := make(chan struct{})
	h.start = func(ctx context.Context, _ browserhost.Options) (nativeBrowserHost, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	done := make(chan error, 1)
	go func() { done <- h.Start(context.Background(), agenttool.NativeBrowserRequest{}) }()
	<-started
	h.Close()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("pending startup returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel the pending browser startup")
	}
}

func TestNativeBrowserClosesIfGUIStopsDuringStartup(t *testing.T) {
	h := newNativeBrowserHosts()
	fake := &fakeNativeHost{done: make(chan struct{})}
	started, release := make(chan struct{}), make(chan struct{})
	h.start = func(context.Context, browserhost.Options) (nativeBrowserHost, error) {
		close(started)
		<-release
		return fake, nil
	}
	done := make(chan error, 1)
	go func() { done <- h.Start(context.Background(), agenttool.NativeBrowserRequest{}) }()
	<-started
	h.Close()
	close(release)
	if err := <-done; err == nil {
		t.Fatal("browser started after the GUI host shut down")
	}
	select {
	case <-fake.done:
	default:
		t.Fatal("late browser was not closed")
	}
}
