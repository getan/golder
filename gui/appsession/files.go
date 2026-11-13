package appsession

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	maxProjectDirectoryEntries = 1000
	maxProjectDirectoryScan    = 4096
	maxProjectPreviewBytes     = 512 << 10
)

type ProjectFileEntry struct {
	Name    string
	Path    string
	Dir     bool
	Symlink bool
	Size    int64
}

type ProjectFilePreview struct {
	Path      string
	Text      string
	Digest    string
	Binary    bool
	Truncated bool
}

var hiddenGeneratedDirectories = map[string]bool{
	".git": true, ".golder": true, ".cache": true,
	"node_modules": true, "vendor": true, "dist": true, "build": true,
}

// ListProjectFiles lists a directory without following symlinks or dotfiles.
func (c *Controller) ListProjectFiles(relative string) ([]ProjectFileEntry, error) {
	return c.ListProjectFilesWithHidden(relative, false)
}

// ListProjectFilesWithHidden optionally includes dotfiles, still excluding
// large generated non-dot directories and never following symbolic links.
func (c *Controller) ListProjectFilesWithHidden(relative string, showHidden bool) ([]ProjectFileEntry, error) {
	c.mu.Lock()
	root := c.cwd
	c.mu.Unlock()
	return listFilesAt(root, relative, showHidden)
}

func listFilesAt(root, relative string, showHidden bool) ([]ProjectFileEntry, error) {
	dir, clean, err := secureProjectPath(root, relative)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("project explorer path is not a directory")
	}
	handle, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	out := make([]ProjectFileEntry, 0, maxProjectDirectoryEntries)
	for scanned := 0; scanned < maxProjectDirectoryScan && len(out) < maxProjectDirectoryEntries; {
		entries, readErr := handle.ReadDir(min(256, maxProjectDirectoryScan-scanned))
		scanned += len(entries)
		for _, entry := range entries {
			if len(out) == maxProjectDirectoryEntries {
				break
			}
			hidden := strings.HasPrefix(entry.Name(), ".")
			if hidden && !showHidden || entry.IsDir() && !hidden && hiddenGeneratedDirectories[entry.Name()] {
				continue
			}
			entryInfo, infoErr := entry.Info()
			if infoErr != nil {
				continue
			}
			rel := entry.Name()
			if clean != "" {
				rel = filepath.Join(clean, entry.Name())
			}
			out = append(out, ProjectFileEntry{
				Name: entry.Name(), Path: filepath.ToSlash(rel), Dir: entry.IsDir(),
				Symlink: entry.Type()&os.ModeSymlink != 0, Size: entryInfo.Size(),
			})
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// ReadProjectFile returns a bounded UTF-8 preview. Binary files are identified
// without rendering their bytes; symlinks and paths outside the project fail.
func (c *Controller) ReadProjectFile(relative string) (ProjectFilePreview, error) {
	c.mu.Lock()
	root := c.cwd
	c.mu.Unlock()
	return readFileAt(root, relative)
}

func readFileAt(root, relative string) (ProjectFilePreview, error) {
	_, clean, err := secureProjectPath(root, relative)
	if err != nil {
		return ProjectFilePreview{}, err
	}
	project, err := os.OpenRoot(root)
	if err != nil {
		return ProjectFilePreview{}, err
	}
	defer project.Close()
	f, err := project.Open(clean)
	if err != nil {
		return ProjectFilePreview{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return ProjectFilePreview{}, errors.New("project explorer path is not a regular file")
	}
	current, err := project.Lstat(clean)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, current) {
		return ProjectFilePreview{}, errors.New("project file was replaced; reopen it")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxProjectPreviewBytes+1))
	if err != nil {
		return ProjectFilePreview{}, err
	}
	truncated := len(raw) > maxProjectPreviewBytes
	if truncated {
		raw = raw[:maxProjectPreviewBytes]
	}
	preview := ProjectFilePreview{Path: filepath.ToSlash(clean), Truncated: truncated}
	if bytesAreBinary(raw) {
		preview.Binary = true
		return preview, nil
	}
	preview.Text = string(raw)
	if !truncated {
		sum := sha256.Sum256(raw)
		preview.Digest = hex.EncodeToString(sum[:])
	}
	return preview, nil
}

// SaveProjectFile is a direct operator action, not an agent tool call. It uses
// normal OS permissions and refuses to overwrite a file changed since preview.
// The opened root confines path resolution even if a directory is replaced.
func (c *Controller) SaveProjectFile(expectedProject, relative, expectedDigest, text string) (string, error) {
	if len(text) > maxProjectPreviewBytes || !utf8.ValidString(text) || strings.ContainsRune(text, '\x00') {
		return "", errors.New("file is too large or is not valid UTF-8 text")
	}
	if len(expectedDigest) != sha256.Size*2 {
		return "", errors.New("file preview is incomplete; reopen the file before saving")
	}
	c.mu.Lock()
	root, closed := c.cwd, c.closed
	c.mu.Unlock()
	if closed {
		return "", ErrClosed
	}
	if expectedProject == "" || filepath.Clean(expectedProject) != filepath.Clean(root) {
		return "", errors.New("project changed; reopen the file before saving")
	}
	return saveFileAt(root, relative, expectedDigest, text)
}

func saveFileAt(root, relative, expectedDigest, text string) (string, error) {
	if len(text) > maxProjectPreviewBytes || !utf8.ValidString(text) || strings.ContainsRune(text, '\x00') || len(expectedDigest) != sha256.Size*2 {
		return "", errors.New("invalid or incomplete editable file preview")
	}
	_, clean, err := secureProjectPath(root, relative)
	if err != nil || clean == "" {
		if err != nil {
			return "", err
		}
		return "", errors.New("select a regular file to save")
	}
	project, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer project.Close()
	file, err := project.OpenFile(clean, os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("cannot open file for writing: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() {
		return "", errors.New("project file is no longer a regular file")
	}
	original, err := io.ReadAll(io.LimitReader(file, maxProjectPreviewBytes+1))
	if err != nil {
		return "", fmt.Errorf("cannot check file before saving: %w", err)
	}
	if len(original) > maxProjectPreviewBytes || bytesAreBinary(original) {
		return "", errors.New("file changed or is not an editable text file")
	}
	before := sha256.Sum256(original)
	if hex.EncodeToString(before[:]) != expectedDigest {
		return "", errors.New("file changed on disk; reopen it before saving")
	}
	current, err := project.Lstat(clean)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, current) {
		return "", errors.New("file was replaced; reopen it before saving")
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	if _, err = io.WriteString(file, text); err != nil {
		return "", fmt.Errorf("save may be incomplete: %w", err)
	}
	if err = file.Truncate(int64(len(text))); err != nil {
		return "", fmt.Errorf("save may be incomplete: %w", err)
	}
	if err = file.Sync(); err != nil {
		return "", fmt.Errorf("save may be incomplete: %w", err)
	}
	after := sha256.Sum256([]byte(text))
	return hex.EncodeToString(after[:]), nil
}

func secureProjectPath(root, relative string) (path, clean string, err error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", "", errors.New("project folder is unavailable")
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", "", err
	}
	clean = filepath.Clean(filepath.FromSlash(strings.TrimSpace(relative)))
	if clean == "." {
		clean = ""
	}
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", "", errors.New("project explorer path escapes the project")
	}
	path = root
	if clean == "" {
		return path, clean, nil
	}
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		path = filepath.Join(path, part)
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return "", "", statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", "", errors.New("project explorer does not follow symbolic links")
		}
	}
	return path, clean, nil
}

func bytesAreBinary(raw []byte) bool {
	if !utf8.Valid(raw) {
		return true
	}
	for _, b := range raw {
		if b == 0 {
			return true
		}
	}
	return false
}
