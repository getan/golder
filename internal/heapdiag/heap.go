// Package heapdiag provides opt-in, file-only Go heap diagnostics.
package heapdiag

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strings"
)

const envDir = "GOLDER_HEAP_PROFILE_DIR"

// writeProfile collects live heap objects after a GC, then publishes the
// complete profile under a unique name. Incomplete files are removed.
func writeProfile(dir string) (path string, err error) {
	f, err := os.CreateTemp(dir, fmt.Sprintf("golder-heap-%d-*.partial", os.Getpid()))
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	runtime.GC()
	if err = pprof.WriteHeapProfile(f); err != nil {
		_ = f.Close()
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	path = strings.TrimSuffix(tmp, ".partial") + ".pprof"
	if err = os.Rename(tmp, path); err != nil {
		return "", err
	}
	return path, nil
}

func profileDir() (string, error) {
	dir := os.Getenv(envDir)
	if dir == "" {
		return "", nil
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", dir)
	}
	return dir, nil
}
