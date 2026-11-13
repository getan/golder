package selfupdate

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMonitorChecksOnEachTickAndStops(t *testing.T) {
	if CheckInterval != time.Hour {
		t.Fatal("background checks must be hourly")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time)
	checks := make(chan struct{}, 2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		monitorTicks(ctx, ticks, func() { checks <- struct{}{} })
	}()
	for range 2 {
		select {
		case ticks <- time.Time{}:
		case <-time.After(time.Second):
			t.Fatal("monitor did not accept a tick")
		}
		select {
		case <-checks:
		case <-time.After(time.Second):
			t.Fatal("monitor did not run its check")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("monitor did not stop on cancellation")
	}
}

func TestMonitorIgnoresQueuedTicksAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan time.Time, 1)
	ticks <- time.Time{}
	cancel()
	monitorTicks(ctx, ticks, func() { t.Fatal("check ran after cancellation") })
}

func TestHourlyCheckBypassesFreshStartupCacheAndPreservesItOnFailure(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	writeCache("v1.5.94")
	calls := 0
	tag, err := checkLatest(context.Background(), "1.5.93", func(_ context.Context, current string) (string, error) {
		calls++
		if current != "1.5.93" {
			t.Fatal("running version was not forwarded")
		}
		return "v1.5.95", nil
	})
	if err != nil || tag != "v1.5.95" || calls != 1 {
		t.Fatalf("hourly check was blocked by the startup TTL: %q %v calls=%d", tag, err, calls)
	}
	for _, result := range []string{"", "invalid", "v1.5.96"} {
		_, err := checkLatest(context.Background(), "1.5.93", func(context.Context, string) (string, error) {
			if result == "v1.5.96" {
				return result, errors.New("offline")
			}
			return result, nil
		})
		if err == nil {
			t.Fatal("invalid metadata or request failure was accepted")
		}
		if cached, _ := CachedLatest(); cached != "v1.5.95" {
			t.Fatalf("failed check overwrote cache with %q", cached)
		}
	}
}

func TestCheckLatestSkipsSourceBuildAndCancelledRequests(t *testing.T) {
	fetch := func(context.Context, string) (string, error) {
		t.Fatal("unnecessary update request")
		return "", nil
	}
	if tag, err := checkLatest(context.Background(), "dev", fetch); tag != "" || err != nil {
		t.Fatalf("source build check=%q %v", tag, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := checkLatest(ctx, "1.5.93", fetch); err != context.Canceled {
		t.Fatalf("cancelled request error=%v", err)
	}
}
