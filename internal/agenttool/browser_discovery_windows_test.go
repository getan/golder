//go:build windows

package agenttool

import (
	"path/filepath"
	"testing"
)

func TestCanonicalPipeEndpoint(t *testing.T) {
	path := `\\.\pipe\golder-view-0123456789ab`
	if got, ok := canonicalPipeEndpoint(path); !ok || got != path {
		t.Fatalf("canonicalPipeEndpoint(%q) = %q, %v", path, got, ok)
	}
	if _, ok := canonicalPipeEndpoint(`C:\tmp\control.sock`); ok {
		t.Fatal("filesystem path accepted as pipe")
	}
}

func TestValidBrowserMetadataEndpointRejectsUnknownPipe(t *testing.T) {
	root := t.TempDir()
	meta := browserMetadata{
		Socket:  `\\.\pipe\golder-view-000000000000`,
		Profile: filepath.Join(root, "profile"),
	}
	if validBrowserMetadataEndpoint(root, meta) {
		t.Fatal("unknown pipe accepted")
	}
}
