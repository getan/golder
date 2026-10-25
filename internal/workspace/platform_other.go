//go:build !darwin && !linux

package workspace

import (
	"errors"
	"os"
	"os/exec"
)

func supported() error {
	return errors.New("workspace: the integrated terminal workspace currently requires macOS or Linux")
}

type stateLease struct{}

var errLeaseBusy = errors.New("workspace lease is in use")

func lockPath(string) (*stateLease, error)        { return nil, supported() }
func openLease(string, bool) (*stateLease, error) { return nil, supported() }
func lockState(string) (*stateLease, error)       { return nil, supported() }
func (*stateLease) Close()                        {}
func refusedSocket(error) bool                    { return false }

func detachCommand(*exec.Cmd) {}

func openLog(string) (*os.File, error) { return nil, supported() }
