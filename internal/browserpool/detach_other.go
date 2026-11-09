//go:build !darwin && !linux

package browserpool

import "os/exec"

func detach(*exec.Cmd) {}
