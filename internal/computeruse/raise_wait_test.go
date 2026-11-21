package computeruse

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAfterPinnedRaise(t *testing.T) {
	calls := 0
	err := afterPinnedRaise(context.Background(), time.Second, time.Millisecond, func() (bool, error) {
		calls++
		return calls == 3, nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("deferred activation: calls=%d err=%v", calls, err)
	}
	calls = 0
	err = afterPinnedRaise(context.Background(), time.Second, time.Millisecond, func() (bool, error) {
		calls++
		return false, ErrTargetLost
	})
	if calls != 1 || !errors.Is(err, ErrTargetLost) {
		t.Fatalf("refusal retried or hidden: %d %v", calls, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	never := func() (bool, error) { t.Fatal("unexpected focus action"); return false, nil }
	if !errors.Is(afterPinnedRaise(ctx, time.Second, time.Millisecond, never), context.Canceled) || !errors.Is(afterPinnedRaise(context.Background(), 0, time.Millisecond, never), errRaiseNotVisible) {
		t.Fatal("ignored cancellation or deadline")
	}
}
