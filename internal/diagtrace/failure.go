package diagtrace

import (
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"syscall"
)

// ErrorClass is deliberately closed: never format an error into the safe trace.
// Errors may contain URLs, request bodies, terminal input or credentials.
func ErrorClass(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, io.EOF):
		return "eof"
	case errors.Is(err, os.ErrClosed):
		return "closed_file"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, syscall.EPIPE):
		return "broken_pipe"
	case errors.Is(err, syscall.EIO):
		return "io_error"
	case errors.Is(err, syscall.EBADF):
		return "bad_fd"
	case errors.Is(err, syscall.ENOTTY):
		return "not_tty"
	default:
		return "other"
	}
}

// Recover must be deferred directly. It records only a fixed panic category
// and compiled function names/line numbers, never the panic value, arguments
// or source paths. The original panic is rethrown; tracing must not repair or
// swallow a failure. Callers pass constant phase names.
func (t *Trace) Recover(phase string) {
	if value := recover(); value != nil {
		if t != nil {
			kind := "other"
			switch value.(type) {
			case runtime.Error:
				kind = "runtime_error"
			case error:
				kind = "error"
			case string:
				kind = "string"
			}
			t.Event("panic", "phase=%s kind=%s", phase, kind)
			var pcs [16]uintptr
			n := runtime.Callers(2, pcs[:])
			frames := runtime.CallersFrames(pcs[:n])
			for index := 0; ; index++ {
				frame, more := frames.Next()
				t.Event("panic", "frame=%d function=%s line=%d", index, frame.Function, frame.Line)
				if !more {
					break
				}
			}
		}
		panic(value)
	}
}
