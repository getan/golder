//go:build !windows
// +build !windows

package uv

import (
	"io"
	"time"

	"github.com/muesli/cancelreader"
)

// NewCancelReader creates a new [cancelreader.CancelReader] that provides a
// cancelable reader interface that can be used to cancel reads.
func NewCancelReader(r io.Reader) (cancelreader.CancelReader, error) {
	o := inputObserverState.Load()
	if o == nil {
		return cancelreader.NewReader(r) //nolint:wrapcheck
	}
	o.emit(InputObservation{Phase: "cancel_reader.init.begin"})
	start := time.Now()
	reader, err := cancelreader.NewReader(r)
	o.emit(InputObservation{Phase: "cancel_reader.init.end", Failed: err != nil, Duration: time.Since(start)})
	if err != nil {
		return nil, err
	}
	return &observedCancelReader{CancelReader: reader, observer: o}, nil
}

type observedCancelReader struct {
	cancelreader.CancelReader
	observer *inputObserver
}

func (r *observedCancelReader) Read(p []byte) (int, error) {
	r.observer.emit(InputObservation{Phase: "poll.begin"})
	start := time.Now()
	n, err := r.CancelReader.Read(p)
	r.observer.emit(InputObservation{Phase: "poll.end", Bytes: n, Failed: err != nil, Duration: time.Since(start)})
	return n, err
}
