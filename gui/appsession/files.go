package appsession

import (
	"errors"
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
	Binary    bool
	Truncated bool
}

var hiddenGeneratedDirectories = map[string]bool{
	".git": true, ".golder": true, ".cache": true,
	"node_modules": true, "vendor": true, "dist": true, "build": true,
}

// ListProjectFiles lists one project-relative directory without following any
// symlink component. Large/generated state directories are hidden by default;
// ordinary dotfiles such as .gitignore and .github remain visible.
func (c *Controller) ListProjectFiles(relative string) ([]ProjectFileEntry, error) {
	c.mu.Lock()
	root := c.cwd
	c.mu.Unlock()
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
			if entry.IsDir() && hiddenGeneratedDirectories[entry.Name()] {
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
	path, clean, err := secureProjectPath(root, relative)
	if err != nil {
		return ProjectFilePreview{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return ProjectFilePreview{}, err
	}
	if !info.Mode().IsRegular() {
		return ProjectFilePreview{}, errors.New("project explorer path is not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return ProjectFilePreview{}, err
	}
	defer f.Close()
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
	return preview, nil
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
