package uv

import (
	"sync/atomic"
	"time"
)

// InputObservation contains diagnostic metadata only. No input bytes, key text,
// file names, environment values or errors are exposed to the observer.
type InputObservation struct {
	Phase    string
	Bytes    int
	Events   int
	Consumed int
	Type     string
	Failed   bool
	Duration time.Duration
}

type inputObserver struct{ observe func(InputObservation) }

var inputObserverState atomic.Pointer[inputObserver]

// SetInputObserver installs an opt-in observer for readers created afterward.
// It returns a restore function; another install is not overwritten on restore.
// This is a diagnostic seam for embedders, not a second stdin reader or a new
// timeout policy. The callback must be fast and safe for concurrent use.
func SetInputObserver(observe func(InputObservation)) func() {
	var next *inputObserver
	if observe != nil {
		next = &inputObserver{observe: observe}
	}
	previous := inputObserverState.Swap(next)
	return func() { inputObserverState.CompareAndSwap(next, previous) }
}

func (o *inputObserver) emit(e InputObservation) {
	if o != nil {
		o.observe(e)
	}
}
