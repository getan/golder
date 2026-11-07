//go:build unix

package heapdiag

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// StartFromEnv enables SIGUSR2-triggered heap snapshots only when the output
// directory is explicitly configured. Stop unregisters the signal handler and
// waits for an in-flight snapshot; the CLI normally runs it until process exit.
func StartFromEnv(errOut io.Writer) (func(), error) {
	dir, err := profileDir()
	if err != nil || dir == "" {
		return func() {}, err
	}
	signals := make(chan os.Signal, 1)
	done := make(chan struct{})
	finished := make(chan struct{})
	signal.Notify(signals, syscall.SIGUSR2)
	go func() {
		defer close(finished)
		for {
			select {
			case <-done:
				return
			case <-signals:
				if _, err := writeProfile(dir); err != nil && errOut != nil {
					fmt.Fprintf(errOut, "golder: heap profile: %v\n", err)
				}
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			signal.Stop(signals)
			close(done)
			<-finished
		})
	}, nil
}
