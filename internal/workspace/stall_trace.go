package workspace

import (
	"runtime"
	"strings"
	"time"

	"github.com/getan/golder/internal/diagtrace"
)

// traceWorkspaceStall takes one metadata-only stack sample after five seconds
// when explicitly tracing. Function symbols identify a blocked UI/restore path;
// unlike debug.Stack/pprof text, no arguments, source paths or input are logged.
func traceWorkspaceStall(trace *diagtrace.Trace) func() {
	if trace == nil {
		return func() {}
	}
	timer := time.AfterFunc(5*time.Second, func() { traceWorkspaceStacks(trace) })
	return func() { timer.Stop() }
}

func traceWorkspaceStacks(trace *diagtrace.Trace) {
	count, _ := runtime.GoroutineProfile(nil)
	records := make([]runtime.StackRecord, count+32)
	count, ok := runtime.GoroutineProfile(records)
	if !ok {
		trace.Event("workspace.stacks", "sample incomplete; goroutine set changed")
		return
	}
	trace.Event("workspace.stacks", "sample begin goroutines=%d", count)
	for _, record := range records[:count] {
		frames := runtime.CallersFrames(record.Stack())
		var symbols []string
		relevant := false
		for {
			frame, more := frames.Next()
			if frame.Function != "" {
				symbols = append(symbols, frame.Function)
				for _, prefix := range []string{"github.com/getan/golder/internal/workspace.", "github.com/getan/tuios-mini/", "charm.land/bubbletea/", "github.com/charmbracelet/ultraviolet.", "github.com/muesli/cancelreader."} {
					relevant = relevant || strings.HasPrefix(frame.Function, prefix)
				}
			}
			if !more {
				break
			}
		}
		if relevant {
			trace.Event("workspace.stacks", "functions=%s", strings.Join(symbols, " > "))
		}
	}
	trace.Event("workspace.stacks", "sample end")
}
