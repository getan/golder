package appsession

import (
	"errors"
	"path/filepath"
)

// FileBrowser is an immutable, operator-only browsing location. It never
// changes the conversation cwd or grants filesystem authority to Agent tools.
type FileBrowser struct {
	controller             *Controller
	session, project, root string
}

// FileBrowserAt binds manual navigation (including parent folders) to the
// original conversation. Relative child paths remain confined to this location.
func (c *Controller) FileBrowserAt(sessionID, project, root string) (*FileBrowser, error) {
	if root == "" {
		root = project
	}
	if !filepath.IsAbs(root) {
		return nil, errors.New("file browser location must be absolute")
	}
	b := &FileBrowser{controller: c, session: sessionID, project: project}
	if err := b.validate(); err != nil {
		return nil, err
	}
	canonical, _, err := secureProjectPath(root, "")
	if err != nil {
		return nil, err
	}
	b.root = canonical
	return b, nil
}

func (b *FileBrowser) validate() error {
	c := b.controller
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrClosed
	}
	if b.session == "" || c.header.ID != b.session || filepath.Clean(c.cwd) != filepath.Clean(b.project) {
		return errors.New("conversation changed; reopen the file browser")
	}
	return nil
}

func (b *FileBrowser) List(relative string, hidden bool) ([]ProjectFileEntry, error) {
	if err := b.validate(); err != nil {
		return nil, err
	}
	return listFilesAt(b.root, relative, hidden)
}

func (b *FileBrowser) Read(relative string) (ProjectFilePreview, error) {
	if err := b.validate(); err != nil {
		return ProjectFilePreview{}, err
	}
	return readFileAt(b.root, relative)
}

func (b *FileBrowser) Save(relative, digest, text string) (string, error) {
	if err := b.validate(); err != nil {
		return "", err
	}
	return saveFileAt(b.root, relative, digest, text)
}
