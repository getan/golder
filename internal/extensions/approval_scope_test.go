package extensions

import (
	"os"
	"path/filepath"
	"testing"
)

func larkApprovalFixture(t *testing.T) (SourceRef, SourceConfig, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GOLDER_HOME", home)
	const helper = "managed-lark-helper"
	source := executableFixture(t, t.TempDir(), executableName("golder-lark"), helper)
	pkg, err := InstallManagedPackage(PackageInstallOptions{
		ID: "lark", Version: "1.0.0", Source: source, SHA256: checksumOf(helper),
	})
	if err != nil {
		t.Fatal(err)
	}
	larkCLI := executableFixture(t, t.TempDir(), "lark-cli", "official-lark")
	cfg, err := LarkSourceConfig(LarkSourceOptions{
		Package: pkg, LarkCLI: larkCLI, AppID: "cli_test", AppSecretCredential: "lark-app-secret",
		Domains: []string{"docs", "im"}, ProjectHost: "project.feishu.cn",
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := SourceRef{Kind: "mcp", ID: "lark"}
	configPath := filepath.Join(home, "config.toml")
	if err := WriteSource(configPath, ref, &cfg); err != nil {
		t.Fatal(err)
	}
	return ref, cfg, configPath
}

func TestLarkApprovalSurvivesProjectChange(t *testing.T) {
	ref, _, configPath := larkApprovalFixture(t)
	first, err := New(Options{ConfigPath: configPath, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Approve(ref); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := New(Options{ConfigPath: configPath, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if snapshot, ok := reopened.Source(ref); !ok || snapshot.Trust != "approved" {
		t.Fatalf("account-scoped Lark approval did not survive a project change: %+v ok=%t", snapshot, ok)
	}
}

func TestGenericApprovalSurvivesProjectChange(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GOLDER_HOME", home)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ref := SourceRef{Kind: "mcp", ID: "generic"}
	cfg := SourceConfig{Transport: "stdio", Command: exe}
	configPath := filepath.Join(home, "config.toml")
	if err := WriteSource(configPath, ref, &cfg); err != nil {
		t.Fatal(err)
	}
	first, err := New(Options{ConfigPath: configPath, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Approve(ref); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := New(Options{ConfigPath: configPath, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if snapshot, ok := reopened.Source(ref); !ok || snapshot.Trust != "approved" {
		t.Fatalf("account-scoped extension approval did not survive a project change: %+v ok=%t", snapshot, ok)
	}
}
