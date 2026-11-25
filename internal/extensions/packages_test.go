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
	if len(packages) != 2 || packageByID(packages, "docx") == nil || packageByID(packages, "docx").Configured || len(m.List()) != 0 {
		t.Fatalf("package discovery created or hid a source: packages=%+v sources=%+v", packages, m.List())
	}
	if lark := packageByID(packages, "lark"); lark == nil || lark.Configured {
		t.Fatalf("Lark package disappeared: %+v", packages)
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
	if len(packages) != 2 || packageByID(packages, "lark") == nil || !packageByID(packages, "lark").Configured || packageByID(packages, "lark").Source.ID != "lark" {
		t.Fatalf("configured package state = %+v", packages)
	}
}

func packageByID(packages []PackageView, id string) *PackageView {
	for i := range packages {
		if packages[i].ID == id {
			return &packages[i]
		}
	}
	return nil
}
