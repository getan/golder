//go:build windows

package browserpool

import (
	"path/filepath"

	"github.com/getan/golder/internal/privatepath"
)

func browserRuntimeDir(key string, getenv func(string) string) string {
	return filepath.Join(privatepath.RuntimeRoot(getenv), "bh-"+key)
}
