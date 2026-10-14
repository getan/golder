//go:build unix

package grants

// Cross-process serialization for the managed table.
//
// Two golder processes can hold the same grants file (a REPL and a session it
// spawned, two terminals in one checkout). Each loads the table at startup and
// writes it back when a dialog is answered, so without a lock the second writer
// clobbers the first — and a grant the user gave would silently disappear.
//
// The lock is a separate file beside the table, taken with flock(2). It is held
// only across the read-modify-write in saveLocked, never across a dialog: the
// user's thinking time must not block another process.

import (
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// lockWait bounds how long a writer waits for another process. Contention is
// two golder instances answering a dialog at the same instant, so the wait is
// short by construction; the bound exists so a stale lock cannot hang a run.
const lockWait = 5 * time.Second

// lockFile takes an exclusive lock on path, creating it if needed. The returned
// function releases it; it is safe to call more than once.
//
// A failure to lock is A failure to write, not a silent fallback to writing
// anyway: the caller reports it, and the grant stays session-scoped, which is
// the honest outcome when persistence cannot be guaranteed.
func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock: %w", err)
	}
	deadline := time.Now().Add(lockWait)
	for {
		err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if err != unix.EWOULDBLOCK {
			f.Close()
			return nil, fmt.Errorf("flock: %w", err)
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("another golder is holding the grants lock (%s)", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
	var once bool
	return func() {
		if once {
			return
		}
		once = true
		// Closing the descriptor releases the flock; unlocking first keeps the
		// window between unlock and close from mattering on a reused fd.
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}, nil
}
