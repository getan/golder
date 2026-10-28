package seatbelt

import (
	"errors"
	"path/filepath"
	"regexp"
)

// ExtensionState is a source-scoped mount capability, not an ordinary directory
// grant. Its path is private so deserializing model arguments cannot mint it.
type ExtensionState struct{ path string }

var sourceStateName = regexp.MustCompile(`^(mcp|plugin)-[a-z0-9][a-z0-9_-]{0,31}$`)

// NewExtensionState accepts only the fixed source HOME layout under Golder's
// protected root. The extension manager owns directory creation and its lock.
func NewExtensionState(home string) (*ExtensionState, error) {
	for _, root := range ProtectedGolderPaths() {
		rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(home))
		if err != nil {
			continue
		}
		parent := filepath.Dir(rel)
		if filepath.Base(rel) == "home" && filepath.Dir(parent) == "extensions" && sourceStateName.MatchString(filepath.Base(parent)) {
			return &ExtensionState{path: home}, nil
		}
	}
	return nil, errors.New("extension state must be a source HOME under the protected extensions directory")
}

func (c *ExtensionState) Path() string {
	if c == nil {
		return ""
	}
	return c.path
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !filepath.IsAbs(rel) && (rel == "." || len(rel) < 3 || rel[:3] != ".."+string(filepath.Separator))
}
