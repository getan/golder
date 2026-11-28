// Package proc constructs child processes for the desktop application.
//
// A Windows GUI process has no console. When it starts a console-subsystem
// program, Windows allocates one, and the default console is visible: a short
// command makes a window appear and disappear for as long as the child lives.
// Command and CommandContext apply the platform attributes that keep that
// console hidden. The attributes are a no-op outside Windows.
//
// PTY/ConPTY processes are deliberately excluded: their terminal host owns the
// console, so callers that need a real terminal must keep using the terminal
// launcher instead of this package.
package proc

import (
	"context"
	"os/exec"
)

// Command is exec.Command with the platform's hidden-console attributes.
func Command(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	configure(cmd)
	return cmd
}

// CommandContext is exec.CommandContext with the same hidden-console
// attributes. The context behavior is exactly exec.CommandContext's.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	configure(cmd)
	return cmd
}

// Configure applies the hidden-console attributes to an already built command.
// Use it when a caller needs exec.Command's other setup first, not for PTY
// processes.
func Configure(cmd *exec.Cmd) {
	configure(cmd)
}
