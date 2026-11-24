package extensions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func installLarkFixture(t *testing.T, version, content string) ManagedPackage {
	t.Helper()
	source := executableFixture(t, t.TempDir(), executableName("golder-lark"), content)
	pkg, err := InstallManagedPackage(PackageInstallOptions{ID: "lark", Version: version, Source: source, SHA256: checksumOf(content)})
	if err != nil {
		t.Fatal(err)
	}
	return pkg
}

func TestRebindLarkSourceKeepsSettingsAndRevokesApproval(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GOLDER_HOME", home)
	configPath := filepath.Join(home, "config.toml")
	oldPkg := installLarkFixture(t, "1.0.0", "helper-v1")
	larkCLI := executableFixture(t, t.TempDir(), "lark-cli", "official-cli")
	cfg, err := LarkSourceConfig(LarkSourceOptions{
		Package: oldPkg, LarkCLI: larkCLI, AppID: "cli_test", AppSecretCredential: "lark-app-secret",
		Domains: []string{"docs", "im"}, ProjectHost: "project.feishu.cn",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteSource(configPath, LarkSourceRef, &cfg); err != nil {
		t.Fatal(err)
	}
	manager, err := New(Options{ConfigPath: configPath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.Approve(LarkSourceRef); err != nil {
		t.Fatal(err)
	}
	if err := manager.Enable(LarkSourceRef); err != nil {
		t.Fatal(err)
	}
	loginHome := filepath.Join(home, "extensions", "mcp-lark", "home")
	if err := os.MkdirAll(loginHome, 0o700); err != nil {
		t.Fatal(err)
	}

	newPkg := installLarkFixture(t, "1.1.0", "helper-v2")
	if err := RebindLarkSource(manager, newPkg); err != nil {
		t.Fatal(err)
	}

	updated, err := manager.Config(LarkSourceRef)
	if err != nil {
		t.Fatal(err)
	}
	resolvedCLI, err := filepath.EvalSymlinks(larkCLI)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Command != newPkg.Executable {
		t.Fatalf("command = %q, want %q", updated.Command, newPkg.Executable)
	}
	if updated.Env["LARK_MCP_APP_SECRET"] != "${credential:lark-app-secret}" ||
		updated.Env["LARK_MCP_APP_ID"] != "cli_test" ||
		updated.Env["LARK_MCP_CLI"] != resolvedCLI ||
		updated.Env["LARK_MCP_DOMAINS"] != "docs,im" ||
		updated.Env["MEEGLE_HOST"] != "project.feishu.cn" {
		t.Fatalf("settings were not preserved: %+v", updated.Env)
	}
	snapshot, ok := manager.Source(LarkSourceRef)
	if !ok || snapshot.Enabled || snapshot.Trust == "approved" {
		t.Fatalf("replacement was approved: %+v", snapshot)
	}
	if _, err := os.Stat(loginHome); err != nil {
		t.Fatalf("login state was removed: %v", err)
	}
	if _, err := os.Stat(oldPkg.Executable); err != nil {
		t.Fatalf("previous helper was removed: %v", err)
	}
}

func TestRebindLarkSourceRefusesUnconfiguredOrUnmanagedSources(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GOLDER_HOME", home)
	configPath := filepath.Join(home, "config.toml")
	manager, err := New(Options{ConfigPath: configPath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	pkg := installLarkFixture(t, "1.1.0", "helper-v2")
	if err := RebindLarkSource(manager, pkg); err == nil || !strings.Contains(err.Error(), "configure Lark") {
		t.Fatalf("unconfigured rebind error = %v", err)
	}

	larkCLI := executableFixture(t, t.TempDir(), "lark-cli", "official-cli")
	cfg := SourceConfig{
		Command: "golder-lark", Transport: "stdio",
		Env: map[string]string{"LARK_MCP_CLI": larkCLI, "LARK_MCP_APP_ID": "cli_test"},
	}
	if err := WriteSource(configPath, LarkSourceRef, &cfg); err != nil {
		t.Fatal(err)
	}
	manager2, err := New(Options{ConfigPath: configPath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager2.Close() })
	if err := RebindLarkSource(manager2, pkg); err == nil {
		t.Fatal("rebind accepted a source without a named App Secret credential")
	}
}
