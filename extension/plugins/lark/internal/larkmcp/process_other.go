//go:build !unix

package larkmcp

import (
	"os/exec"

	"github.com/getan/golder/internal/proc"
)

func configureChild(cmd *exec.Cmd) {
	proc.Configure(cmd)
}
