package computeruse

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestWindowInventoryGrowsWithoutTruncation(t *testing.T) {
	for _, total := range []int{0, 127, 128, 129, 250, 8192} {
		calls := 0
		got, err := collectWindowInventory(context.Background(), func(buf []int) int {
			calls++
			for i := range min(len(buf), total) {
				buf[i] = i + 1
			}
			return total
		})
		if err != nil || len(got) != total || total > 0 && got[total-1] != total {
			t.Fatalf("total=%d: len=%d calls=%d err=%v", total, len(got), calls, err)
		}
		if total <= 128 && calls != 1 || total > 128 && calls < 2 {
			t.Fatalf("total=%d: unexpected number of reads %d", total, calls)
		}
	}
}

func TestWindowInventoryDoesNotAuthorizeTruncatedSnapshot(t *testing.T) {
	got, err := collectWindowInventory(context.Background(), func([]int) int { return 8193 })
	if got != nil || err == nil || !strings.Contains(err.Error(), "safe limit") {
		t.Fatalf("oversized inventory = %v, %v", got, err)
	}
	got, err = collectWindowInventory(context.Background(), func([]int) int { return -1 })
	if got != nil || err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("unavailable inventory = %v, %v", got, err)
	}
}

func TestWindowInventoryUsesFinalCompleteSnapshot(t *testing.T) {
	calls := 0
	got, err := collectWindowInventory(context.Background(), func(buf []int) int {
		calls++
		total := 129
		if calls == 2 {
			total = 257 // windows appeared while the inventory grew
		} else if calls == 3 {
			total = 200
		}
		for i := range min(len(buf), total) {
			buf[i] = calls
		}
		return total
	})
	if err != nil || calls != 3 || len(got) != 200 || got[0] != 3 || got[len(got)-1] != 3 {
		t.Fatalf("incomplete or mixed snapshots: calls=%d len=%d err=%v", calls, len(got), err)
	}
}

func TestWindowInventoryHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	_, err := collectWindowInventory(ctx, func([]int) int {
		cancel()
		return 1
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled inventory = %v", err)
	}
}
