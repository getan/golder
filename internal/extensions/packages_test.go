package extensions

import (
	"path/filepath"
	"testing"
)

func TestLarkPackageIsVisibleWithoutGrantingASource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GOLDER_HOME", home)
	m, err := New(Options{ConfigPath: filepath.Join(home, "config.toml")})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	packages := m.PackageViews()
	if len(packages) != 1 || packages[0].ID != "lark" || packages[0].Configured || len(m.List()) != 0 {
		t.Fatalf("package discovery created or hid a source: packages=%+v sources=%+v", packages, m.List())
	}

	path := filepath.Join(home, "config.toml")
	if err := WriteSource(path, SourceRef{Kind: "mcp", ID: "lark"}, &SourceConfig{
		Transport: "stdio", Command: filepath.Join(home, "golder-lark"),
	}); err != nil {
		t.Fatal(err)
	}
	configured, err := New(Options{ConfigPath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer configured.Close()
	packages = configured.PackageViews()
	if len(packages) != 1 || !packages[0].Configured || packages[0].Source.ID != "lark" {
		t.Fatalf("configured package state = %+v", packages)
	}
}
