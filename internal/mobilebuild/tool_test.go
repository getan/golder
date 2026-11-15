package mobilebuild

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/agentcore"
)

func TestParseGitHubURL(t *testing.T) {
	for _, raw := range []string{
		"https://github.com/getan/golder-mobile-test.git",
		"git@github.com:getan/golder-mobile-test.git",
		"ssh://git@github.com/getan/golder-mobile-test",
	} {
		owner, repo, ok := parseGitHubURL(raw)
		if !ok || owner != "getan" || repo != "golder-mobile-test" {
			t.Fatalf("%q => %q/%q ok=%t", raw, owner, repo, ok)
		}
	}
	if _, _, ok := parseGitHubURL("https://gitlab.com/getan/repo.git"); ok {
		t.Fatal("non-GitHub URL accepted")
	}
}

func TestInstallToolUsesDownloadedAPK(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, buildDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	apk := filepath.Join(dir, "app-debug.apk")
	if err := os.WriteFile(apk, []byte("fake apk"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool, err := NewInstall(Config{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	result, err := tool.Execute(context.Background(), "call", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("install tool failed: %s", agentcore.ContentToText(result.Content))
	}
	if got := agentcore.ContentToText(result.Content); !strings.Contains(got, "INSTALL_APK="+apk) {
		t.Fatalf("install output missing APK marker: %q", got)
	}
}

func TestInstallToolRejectsOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.apk")
	if err := os.WriteFile(outside, []byte("fake apk"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool, err := NewInstall(Config{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	result, err := tool.Execute(context.Background(), "call", []byte(`{"path":"`+outside+`"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("outside-workspace APK was accepted")
	}
}
