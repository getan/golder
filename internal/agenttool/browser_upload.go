package agenttool

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// An upload hands local file bytes to a page. The call may name only workspace
// files: an absolute path, a symlink that leaves the workspace, a directory or
// an oversized file is refused before anything reaches the browser.
const (
	browserUploadMaxFiles     = 20
	browserUploadMaxFileBytes = 25 << 20
	browserUploadMaxTotal     = 50 << 20
)

func resolveBrowserUploadFiles(cwd string, files []string) ([]string, error) {
	if len(files) == 0 {
		return nil, errors.New("upload needs at least one workspace-relative file")
	}
	if len(files) > browserUploadMaxFiles {
		return nil, fmt.Errorf("upload accepts at most %d files", browserUploadMaxFiles)
	}
	root, err := filepath.Abs(cwd)
	if err != nil {
		return nil, fmt.Errorf("upload workspace: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("upload workspace: %w", err)
	}
	out := make([]string, 0, len(files))
	var total int64
	for _, raw := range files {
		name := strings.TrimSpace(raw)
		if name == "" {
			return nil, errors.New("upload file name is empty")
		}
		if filepath.IsAbs(name) {
			return nil, fmt.Errorf("upload file %q must be workspace-relative", raw)
		}
		real, evalErr := filepath.EvalSymlinks(filepath.Join(root, filepath.Clean(name)))
		if evalErr != nil {
			return nil, fmt.Errorf("upload file %q: %w", raw, evalErr)
		}
		rel, relErr := filepath.Rel(root, real)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("upload file %q is outside the workspace", raw)
		}
		info, statErr := os.Stat(real)
		if statErr != nil {
			return nil, fmt.Errorf("upload file %q: %w", raw, statErr)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("upload file %q is not a regular file", raw)
		}
		if info.Size() > browserUploadMaxFileBytes {
			return nil, fmt.Errorf("upload file %q is larger than %d MiB", raw, browserUploadMaxFileBytes>>20)
		}
		total += info.Size()
		if total > browserUploadMaxTotal {
			return nil, fmt.Errorf("upload total exceeds %d MiB", browserUploadMaxTotal>>20)
		}
		out = append(out, real)
	}
	return out, nil
}
