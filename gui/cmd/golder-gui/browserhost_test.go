package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/getan/kitty-browse/browserhost"

	"github.com/getan/golder/internal/agenttool"
)

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
