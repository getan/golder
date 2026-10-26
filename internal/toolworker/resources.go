package toolworker

import (
	"context"
	"fmt"
)

// The worker pool bounds only Golder's own one-call tool-worker subprocesses.
// bash, sub-agents and command-line children are external processes and are
// deliberately not part of this pool.
//
// apply_patch and view_image consume the SAME global slots as read tools; their
// class lanes add the stricter single-task rule. Acquire the class lane first,
// then a shared slot, so a queued patch never holds a shared slot while it waits
// behind another patch.
const (
	workerPoolSize   = 4
	writeWorkerLimit = 1
	imageWorkerLimit = 1
)

var (
	workerSlots = make(chan struct{}, workerPoolSize)
	writeSlots  = make(chan struct{}, writeWorkerLimit)
	imageSlots  = make(chan struct{}, imageWorkerLimit)
)

type workerLane int

const (
	laneShared workerLane = iota
	laneWrite
	laneImage
)

// workerLaneFor maps a tool to its class lane. Unknown tools only consume a
// shared slot; this keeps the pool correct if a future file tool is wrapped.
func workerLaneFor(name string) workerLane {
	switch name {
	case "apply_patch", "write":
		return laneWrite
	case "view_image":
		return laneImage
	default:
		return laneShared
	}
}

func acquireWorkerSlot(ctx context.Context, name string) (func(), error) {
	var classSlot chan struct{}
	switch workerLaneFor(name) {
	case laneWrite:
		classSlot = writeSlots
	case laneImage:
		classSlot = imageSlots
	}
	if classSlot != nil {
		if err := waitWorkerSlot(ctx, classSlot); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
	}
	if err := waitWorkerSlot(ctx, workerSlots); err != nil {
		if classSlot != nil {
			<-classSlot
		}
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return func() {
		<-workerSlots
		if classSlot != nil {
			<-classSlot
		}
	}, nil
}

func waitWorkerSlot(ctx context.Context, slot chan struct{}) error {
	select {
	case slot <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("cancelled while waiting for a tool worker slot: %w", ctx.Err())
	}
}
