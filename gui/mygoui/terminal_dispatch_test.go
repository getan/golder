package mygoui

import (
	"context"
	"errors"
	"testing"
)

func TestTerminalDispatchCancellationBeforeStartDoesNotMutate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	queued := make(chan func(), 1)
	result := make(chan error, 1)
	changed := false
	go func() {
		_, err := dispatchTerminalUI(ctx, func(f func()) { queued <- f }, func() (any, error) {
			changed = true
			return nil, nil
		})
		result <- err
	}()
	f := <-queued
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled dispatch = %v", err)
	}
	f()
	if changed {
		t.Fatal("canceled queued UI operation changed state")
	}
}

func TestTerminalDispatchWaitsForStartedMutation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	queued := make(chan func(), 1)
	started, release := make(chan struct{}), make(chan struct{})
	result := make(chan terminalUIReply, 1)
	go func() {
		value, err := dispatchTerminalUI(ctx, func(f func()) { queued <- f }, func() (any, error) {
			close(started)
			<-release
			return "finished", nil
		})
		result <- terminalUIReply{value: value, err: err}
	}()
	f := <-queued
	go f()
	<-started
	cancel()
	close(release)
	got := <-result
	if got.err != nil || got.value != "finished" {
		t.Fatalf("started dispatch result = %+v", got)
	}
}
