package toolworker

import (
	"context"
	"testing"
	"time"
)

func TestWorkerLaneClassification(t *testing.T) {
	for name, want := range map[string]workerLane{
		"read": laneShared, "grep": laneShared, "find": laneShared, "ls": laneShared,
		"apply_patch": laneWrite, "write": laneWrite, "view_image": laneImage,
		"future_tool": laneShared,
	} {
		if got := workerLaneFor(name); got != want {
			t.Errorf("%s lane = %v, want %v", name, got, want)
		}
	}
}

func TestSharedWorkerPoolBlocksWithoutEvicting(t *testing.T) {
	oldWorker, oldWrite, oldImage := workerSlots, writeSlots, imageSlots
	workerSlots = make(chan struct{}, 1)
	writeSlots = make(chan struct{}, 1)
	imageSlots = make(chan struct{}, 1)
	t.Cleanup(func() { workerSlots, writeSlots, imageSlots = oldWorker, oldWrite, oldImage })

	release, err := acquireWorkerSlot(context.Background(), "read")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := acquireWorkerSlot(ctx, "grep"); err == nil {
		t.Fatal("a second worker acquired the full pool; it must wait, not evict")
	}
	release()
	releaseAgain, err := acquireWorkerSlot(context.Background(), "find")
	if err != nil {
		t.Fatalf("released slot was not reusable: %v", err)
	}
	releaseAgain()
}

func TestWriteAndImageLanesShareTheGlobalPool(t *testing.T) {
	oldWorker, oldWrite, oldImage := workerSlots, writeSlots, imageSlots
	workerSlots = make(chan struct{}, 2)
	writeSlots = make(chan struct{}, 1)
	imageSlots = make(chan struct{}, 1)
	t.Cleanup(func() { workerSlots, writeSlots, imageSlots = oldWorker, oldWrite, oldImage })

	patch, err := acquireWorkerSlot(context.Background(), "apply_patch")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := acquireWorkerSlot(ctx, "apply_patch"); err == nil {
		t.Fatal("two apply_patch workers acquired the write lane")
	}
	read, err := acquireWorkerSlot(context.Background(), "read")
	if err != nil {
		t.Fatalf("read should use the other shared slot: %v", err)
	}
	imageCtx, imageCancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer imageCancel()
	if _, err := acquireWorkerSlot(imageCtx, "view_image"); err == nil {
		t.Fatal("image worker exceeded the shared pool")
	}
	read()
	image, err := acquireWorkerSlot(context.Background(), "view_image")
	if err != nil {
		t.Fatalf("released shared slot was not reusable by image: %v", err)
	}
	image()
	patch()
}
