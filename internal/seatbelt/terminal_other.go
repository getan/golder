//go:build !darwin

package seatbelt

import "errors"

// Linux's private devpts namespace already confines terminal device discovery.
// Its bwrap runner does not use the macOS pre-sandbox terminal binder.
func ExecTerminalSandbox(_ []string) error {
	return errors.New("terminal sandbox bootstrap is macOS-only")
}
