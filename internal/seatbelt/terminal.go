package seatbelt

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// TerminalBootstrapFlag enters the trusted pre-sandbox terminal binder. It is
// not a tool API: the CLI dispatches it before loading configuration or state.
const TerminalBootstrapFlag = "--golder-terminal-sandbox"

var terminalBootstrap struct {
	sync.RWMutex
	executable string
}

// ConfigureTerminalLauncher registers an entry point that implements
// TerminalBootstrapFlag. Do not assume an arbitrary SDK host implements it.
// A missing launcher fails closed when a terminal-capable pane is requested.
func ConfigureTerminalLauncher(executable string) error {
	terminalBootstrap.Lock()
	defer terminalBootstrap.Unlock()
	terminalBootstrap.executable = ""
	if executable == "" {
		return nil
	}
	abs, err := filepath.Abs(executable)
	if err != nil {
		return err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("terminal sandbox launcher is not an executable file: %s", abs)
	}
	terminalBootstrap.executable = abs
	return nil
}

func terminalSandboxArgv(profile string, argv []string) ([]string, error) {
	terminalBootstrap.RLock()
	executable := terminalBootstrap.executable
	terminalBootstrap.RUnlock()
	if executable == "" {
		return nil, errors.New("terminal sandbox launcher is not configured; refusing an unbound pane")
	}
	return append([]string{executable, TerminalBootstrapFlag, profile, "--"}, argv...), nil
}
