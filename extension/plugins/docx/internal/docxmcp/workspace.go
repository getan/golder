package docxmcp

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Workspace confines every read and write to one root. The model never supplies
// an absolute path or a path that resolves outside this root.
type Workspace struct {
	root string
}

func NewWorkspace(root string) (*Workspace, error) {
	if strings.TrimSpace(root) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, errf(codePathInvalid, "cannot determine the working directory: %v", err)
		}
		root = cwd
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, errf(codePathInvalid, "invalid workspace root: %v", err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, errf(codePathNotFound, "workspace root does not exist")
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() {
		return nil, errf(codePathInvalid, "workspace root is not a directory")
	}
	return &Workspace{root: real}, nil
}

func (w *Workspace) Root() string { return w.root }

func cleanRelative(rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", errf(codeInvalidArguments, "path is required")
	}
	if strings.ContainsRune(rel, 0) {
		return "", errf(codePathInvalid, "path contains a NUL byte")
	}
	if strings.Contains(rel, `\`) {
		return "", errf(codePathInvalid, "use forward slashes in a relative path")
	}
	if filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" {
		return "", errf(codePathOutside, "absolute paths are not accepted; use a workspace-relative path")
	}
	clean := filepath.Clean(rel)
	if clean == "." {
		return "", errf(codePathInvalid, "path must name a file")
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errf(codePathOutside, "path escapes the workspace")
	}
	return clean, nil
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func (w *Workspace) resolveExisting(rel, wantExt string) (string, os.FileInfo, error) {
	clean, err := cleanRelative(rel)
	if err != nil {
		return "", nil, err
	}
	if !strings.EqualFold(filepath.Ext(clean), wantExt) {
		return "", nil, errf(codeUnsupportedExt, "only %s files are accepted", wantExt)
	}
	full := filepath.Join(w.root, clean)
	info, err := os.Lstat(full)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil, errf(codePathNotFound, "file not found")
		}
		return "", nil, errf(codePathInvalid, "cannot inspect path: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", nil, errf(codeSymlinkRejected, "symlinks are not accepted")
	}
	real, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", nil, errf(codePathNotFound, "file not found")
	}
	if !within(w.root, real) {
		return "", nil, errf(codePathOutside, "path resolves outside the workspace")
	}
	info, err = os.Stat(real)
	if err != nil || !info.Mode().IsRegular() {
		return "", nil, errf(codePathNotRegular, "path is not a regular file")
	}
	return real, info, nil
}

// ReadDocx returns a bounded copy of a workspace .docx file.
func (w *Workspace) ReadDocx(rel string) (string, []byte, error) {
	path, info, err := w.resolveExisting(rel, ".docx")
	if err != nil {
		return "", nil, err
	}
	if info.Size() > maxInputBytes {
		return "", nil, errf(codeFileTooLarge, "document exceeds the %d MiB input limit", maxInputBytes>>20)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, errf(codePathInvalid, "cannot read document: %v", err)
	}
	if len(data) > maxInputBytes {
		return "", nil, errf(codeFileTooLarge, "document exceeds the %d MiB input limit", maxInputBytes>>20)
	}
	return path, data, nil
}

// ReadImage returns a bounded copy of a workspace image file.
func (w *Workspace) ReadImage(rel string) (string, []byte, error) {
	clean, err := cleanRelative(rel)
	if err != nil {
		return "", nil, err
	}
	switch strings.ToLower(filepath.Ext(clean)) {
	case ".png", ".jpg", ".jpeg", ".gif":
	default:
		return "", nil, errf(codeImageInvalid, "only PNG, JPEG and GIF images are accepted")
	}
	path, info, err := w.resolveExisting(rel, strings.ToLower(filepath.Ext(clean)))
	if err != nil {
		return "", nil, err
	}
	if info.Size() > maxImageBytes {
		return "", nil, errf(codeImageTooLarge, "image exceeds the %d MiB limit", maxImageBytes>>20)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, errf(codePathInvalid, "cannot read image: %v", err)
	}
	if len(data) > maxImageBytes {
		return "", nil, errf(codeImageTooLarge, "image exceeds the %d MiB limit", maxImageBytes>>20)
	}
	return path, data, nil
}

// PrepareOutput validates a destination and returns the canonical path. The
// final component must not be a symlink; the parent must resolve inside root.
func (w *Workspace) PrepareOutput(rel string, overwrite bool) (string, error) {
	clean, err := cleanRelative(rel)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(filepath.Ext(clean), ".docx") {
		return "", errf(codeUnsupportedExt, "output must end in .docx")
	}
	full := filepath.Join(w.root, clean)
	dir := filepath.Dir(full)
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", errf(codePathNotFound, "output directory does not exist")
	}
	if !within(w.root, realDir) {
		return "", errf(codePathOutside, "output directory resolves outside the workspace")
	}
	info, err := os.Stat(realDir)
	if err != nil || !info.IsDir() {
		return "", errf(codePathInvalid, "output parent is not a directory")
	}
	target := filepath.Join(realDir, filepath.Base(clean))
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errf(codeSymlinkRejected, "refusing to replace a symlink")
		}
		if !info.Mode().IsRegular() {
			return "", errf(codePathNotRegular, "output is not a regular file")
		}
		if !overwrite {
			return "", errf(codeOutputExists, "output already exists; pass overwrite=true to replace it")
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", errf(codePathInvalid, "cannot inspect output: %v", err)
	}
	return target, nil
}

// WriteAtomic writes data through a sibling temp file and renames it into place,
// so a failure never truncates an existing document.
func WriteAtomic(path string, data []byte) error {
	if len(data) > maxOutputBytes {
		return errf(codeOutputTooLarge, "document exceeds the %d MiB output limit", maxOutputBytes>>20)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".golder-docx-*")
	if err != nil {
		return errf(codeWriteFailed, "cannot create temporary output: %v", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return errf(codeWriteFailed, "cannot secure temporary output: %v", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return errf(codeWriteFailed, "cannot write document: %v", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return errf(codeWriteFailed, "cannot flush document: %v", err)
	}
	if err := tmp.Close(); err != nil {
		return errf(codeWriteFailed, "cannot close document: %v", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return errf(codeWriteFailed, "cannot replace output: %v", err)
	}
	cleanup = false
	return nil
}
