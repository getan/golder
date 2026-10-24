//go:build !darwin && !linux

package workspace

import (
	"errors"
	"os/exec"
)

func supported() error {
	return errors.New("workspace: the integrated terminal workspace currently requires macOS or Linux")
}

type stateLease struct{}

func lockState(string) (*stateLease, error) { return nil, supported() }
func (*stateLease) Close()                  {}
func refusedSocket(error) bool              { return false }

func detachCommand(*exec.Cmd) {}
