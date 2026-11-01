//go:build !unix

package larkmcp

import "os/exec"

func configureChild(*exec.Cmd) {}
