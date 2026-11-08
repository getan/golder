package appsession

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

// ReadTranscriptImage reads a bounded project file for an automatic transcript
// preview. It never follows a path outside project, including through a raced
// symlink. Callers must independently validate the image's format and size.
func ReadTranscriptImage(project, path string, limit int64) ([]byte, error) {
	if project == "" || path == "" || limit <= 0 {
		return nil, errors.New("invalid image path or limit")
	}
	if filepath.IsAbs(path) {
		var err error
		path, err = filepath.Rel(project, path)
		if err != nil {
			return nil, err
		}
	}
	_, clean, err := secureProjectPath(project, path)
	if err != nil || clean == "" {
		return nil, errors.New("image path is outside the project or inaccessible")
	}
	root, err := os.OpenRoot(project)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	before, err := root.Lstat(clean)
	if err != nil || !before.Mode().IsRegular() || before.Size() > limit {
		return nil, errors.New("image is not a regular file or is too large")
	}
	f, err := root.OpenFile(clean, os.O_RDONLY|transcriptNonblock, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) || opened.Size() > limit {
		return nil, errors.New("image file changed or is too large")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("image is too large")
	}
	return data, nil
}
