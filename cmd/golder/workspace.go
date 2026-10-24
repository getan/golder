package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/getan/kitty-browse/browsepane"

	"github.com/getan/golder/internal/cli/workspacecmd"
	workspacehost "github.com/getan/golder/internal/workspace"
)

// runWorkspace owns signals around the parser and runtime. Defer cleanup here,
// not in main: os.Exit must only happen after every pane and socket is closed.
func runWorkspace(args []string, in, out *os.File, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	backend := &workspacehost.Backend{Version: version, Input: in, Output: out}
	defer backend.Close()
	code := workspacecmd.Run(ctx, args, out, stderr, backend)
	if code != workspacecmd.ExitOK {
		return code
	}
	// In headless mode Start returned before the readiness document was
	// printed. Keep the owning process alive until a signal or `workspace stop`.
	if err := backend.Wait(ctx); err != nil {
		if ctx.Err() != nil {
			return workspacecmd.ExitInterrupted
		}
		fmt.Fprintln(stderr, "golder workspace:", err)
		return workspacecmd.ExitFailure
	}
	return workspacecmd.ExitOK
}

func runBrowse(args []string, in *os.File, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := browsepane.Run(ctx, browsepane.Options{
		Args: args, Stdin: in, Stdout: stdout, Stderr: stderr, Name: "golder browse",
		Version: fmt.Sprintf("golder browse %s (commit %s, built %s)", version, commit, date),
	})
	if err == nil || errors.Is(err, context.Canceled) {
		return 0
	}
	fmt.Fprintln(stderr, "golder browse:", err)
	return 1
}
