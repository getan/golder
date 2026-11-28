//go:build !windows

package browserpool

import (
	"path/filepath"

	"github.com/getan/golder/internal/privatepath"
)

func browserRuntimeDir(key string, getenv func(string) string) string {
	runtimeRoot := privatepath.RuntimeRoot(getenv)
	if len(filepath.Join(runtimeRoot, "bh-"+key, "000000000000000000000000.sock")) > 103 {
		runtimeRoot = filepath.Join("/tmp", privatepath.RuntimeName())
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(runtimeRoot))
	if err == nil {
		runtimeRoot = filepath.Join(parent, filepath.Base(runtimeRoot))
	}
	return filepath.Join(runtimeRoot, "bh-"+key)
}
