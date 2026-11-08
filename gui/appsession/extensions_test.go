package appsession

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	larkversion "github.com/getan/golder/extension/plugins/lark/version"
	"github.com/getan/golder/internal/extensions"
	"github.com/getan/golder/internal/provider"
	"github.com/getan/golder/internal/session"
)

func guiExtensionExecutable(t *testing.T, dir, name, content string) string {
	t.Helper()
	if runtime.GOOS == "windows" && filepath.Ext(name) == "" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	mode := os.FileMode(0o700)
	if runtime.GOOS == "windows" {
		mode = 0o600
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestGUIManagedLarkConfigureEnableDisableAndUninstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GOLDER_HOME", home)
	manager, err := extensions.New(extensions.Options{ConfigPath: filepath.Join(home, "config.toml"), Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	controller := New(Options{
		Model: "test-model", ProviderName: "test-provider", Version: "1.5.62", Cwd: t.TempDir(), Store: store,
		Extensions: manager,
	})
	if err := controller.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer controller.Close()

	const helperContent = "managed-lark-helper"
	helper := guiExtensionExecutable(t, t.TempDir(), "golder-lark", helperContent)
	sum := sha256.Sum256([]byte(helperContent))
	if err := controller.InstallLarkPackage(context.Background(), helper, hex.EncodeToString(sum[:]), larkversion.Version); err != nil {
		t.Fatal(err)
	}
	larkCLI := guiExtensionExecutable(t, t.TempDir(), "lark-cli", "official-lark")
	if err := controller.ConfigureLark(context.Background(), LarkExtensionEdit{
		LarkCLI: larkCLI, AppID: "cli_test", CredentialName: "lark-app-secret",
		NewAppSecret: "test-app-secret", Domains: []string{"docs", "im"}, ProjectHost: "project.feishu.cn",
	}); err != nil {
		t.Fatal(err)
	}
	view, err := controller.ExtensionSettings()
	if err != nil {
		t.Fatal(err)
	}
	if !view.Installed || !view.Configured || view.Enabled || !view.CredentialAvailable || view.CredentialName != "lark-app-secret" {
		t.Fatalf("configured view = %+v", view)
	}
	cfg, err := manager.Config(larkSourceRef)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Env["LARK_MCP_APP_SECRET"]; got != "${credential:lark-app-secret}" || strings.Contains(got, "test-app-secret") {
		t.Fatalf("stored secret setting = %q", got)
	}
	credentialBytes, err := os.ReadFile(provider.CredentialFilePath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(credentialBytes), "test-app-secret") {
		t.Fatal("Lark App Secret was stored in plaintext")
	}
	if err := controller.EnableLark(); err != nil {
		t.Fatal(err)
	}
	view, _ = controller.ExtensionSettings()
	if !view.Enabled || !view.Trusted {
		t.Fatalf("enabled view = %+v", view)
	}
	controller.mu.Lock()
	controller.opt.Version = "9.9.9"
	controller.mu.Unlock()
	view, err = controller.ExtensionSettings()
	if err != nil || view.BundledVersion != larkversion.Version || view.UpdateAvailable || !view.Enabled || !view.Trusted {
		t.Fatalf("host upgrade disturbed unchanged helper: %+v, %v", view.ExtensionPackageView, err)
	}
	if err := controller.requireMatchingLarkHelper(); err != nil {
		t.Fatalf("host upgrade blocked the unchanged helper: %v", err)
	}
	if err := controller.DisableLark(); err != nil {
		t.Fatal(err)
	}
	if err := controller.UninstallLark(); err != nil {
		t.Fatal(err)
	}
	view, _ = controller.ExtensionSettings()
	if view.Installed || view.Configured {
		t.Fatalf("uninstalled view = %+v", view)
	}
}

func TestGUIManagedLarkUpdateRebindsSourceAndPreservesPrivateState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GOLDER_HOME", home)
	manager, err := extensions.New(extensions.Options{ConfigPath: filepath.Join(home, "config.toml"), Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	controller := New(Options{
		Model: "test-model", ProviderName: "test-provider", Version: "dev",
		Cwd: t.TempDir(), Store: store, Extensions: manager,
	})
	if err := controller.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer controller.Close()

	const oldContent = "managed-lark-helper-v1"
	oldHelper := guiExtensionExecutable(t, t.TempDir(), "golder-lark", oldContent)
	oldSum := sha256.Sum256([]byte(oldContent))
	if err := controller.InstallLarkPackage(context.Background(), oldHelper, hex.EncodeToString(oldSum[:]), "1.0.0"); err != nil {
		t.Fatal(err)
	}
	larkCLI := guiExtensionExecutable(t, t.TempDir(), "lark-cli", "official-lark")
	edit := LarkExtensionEdit{
		LarkCLI: larkCLI, AppID: "cli_test", CredentialName: "lark-app-secret",
		NewAppSecret: "test-app-secret", Domains: []string{"docs", "im"}, ProjectHost: "project.feishu.cn",
	}
	if err := controller.ConfigureLark(context.Background(), edit); err != nil {
		t.Fatal(err)
	}
	if err := controller.EnableLark(); err != nil {
		t.Fatal(err)
	}
	controller.mu.Lock()
	controller.opt.Version = "2.0.0"
	controller.mu.Unlock()
	privateHome := filepath.Join(home, "extensions", "mcp-lark", "home")
	if err := os.MkdirAll(privateHome, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(privateHome, "login-state-marker")
	if err := os.WriteFile(marker, []byte("keep-login"), 0o600); err != nil {
		t.Fatal(err)
	}

	before, err := controller.ExtensionSettings()
	if err != nil {
		t.Fatal(err)
	}
	if before.BundledVersion != larkversion.Version || before.InstalledVersion != "1.0.0" || before.ActiveVersion != "1.0.0" || !before.UpdateAvailable {
		t.Fatalf("pre-update versions = %+v", before.ExtensionPackageView)
	}
	if err := controller.AuthorizeLark(context.Background(), "lark", nil); err == nil || !strings.Contains(err.Error(), "update the managed") {
		t.Fatalf("mismatched helper reached authorization: %v", err)
	}
	if err := controller.TestLark(context.Background()); err == nil || !strings.Contains(err.Error(), "update the managed") {
		t.Fatalf("mismatched helper reached connection test: %v", err)
	}
	beforeCfg, err := manager.Config(larkSourceRef)
	if err != nil {
		t.Fatal(err)
	}

	const newContent = "managed-lark-helper-v2"
	newHelper := guiExtensionExecutable(t, t.TempDir(), "golder-lark", newContent)
	newSum := sha256.Sum256([]byte(newContent))
	if err := controller.UpdateLarkPackage(context.Background(), newHelper, hex.EncodeToString(newSum[:]), larkversion.Version); err != nil {
		t.Fatal(err)
	}
	after, err := controller.ExtensionSettings()
	if err != nil {
		t.Fatal(err)
	}
	if after.BundledVersion != larkversion.Version || after.InstalledVersion != larkversion.Version || after.ActiveVersion != larkversion.Version || !after.ActiveManaged || after.UpdateAvailable || after.Enabled || after.Trusted {
		t.Fatalf("post-update settings = %+v", after)
	}
	controller.mu.Lock()
	controller.opt.Version = "9.9.9"
	controller.mu.Unlock()
	afterMainUpgrade, err := controller.ExtensionSettings()
	if err != nil || afterMainUpgrade.BundledVersion != larkversion.Version || afterMainUpgrade.UpdateAvailable {
		t.Fatalf("main upgrade changed Lark version or required an update: %+v, %v", afterMainUpgrade.ExtensionPackageView, err)
	}
	cfg, err := manager.Config(larkSourceRef)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cfg.Command, filepath.Join("lark", larkversion.Version, "bin")) || cfg.Enabled || cfg.Env["LARK_MCP_CLI"] != beforeCfg.Env["LARK_MCP_CLI"] || cfg.Env["LARK_MCP_APP_ID"] != beforeCfg.Env["LARK_MCP_APP_ID"] || cfg.Env["LARK_MCP_DOMAINS"] != beforeCfg.Env["LARK_MCP_DOMAINS"] {
		t.Fatalf("rebound source = %+v", cfg)
	}
	if value, err := provider.ResolveCredentialReference(provider.CredentialFilePath(), "lark-app-secret"); err != nil || value != "test-app-secret" {
		t.Fatalf("named credential changed: value=%q err=%v", value, err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "keep-login" {
		t.Fatalf("private login state changed: data=%q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(home, "extensions", "packages", "lark", "1.0.0")); err != nil {
		t.Fatalf("old package was destructively removed: %v", err)
	}
}

func TestGUIOfficialCLIDiscoveryAndInstallUseNativePaths(t *testing.T) {
	oldDiscover, oldInstall := discoverOfficialCLI, installOfficialCLI
	t.Cleanup(func() { discoverOfficialCLI, installOfficialCLI = oldDiscover, oldInstall })
	discoverOfficialCLI = func(kind extensions.OfficialCLIKind, preferred string) (extensions.OfficialCLI, bool) {
		if preferred != "" {
			return extensions.OfficialCLI{Kind: kind, Executable: preferred + "-resolved"}, true
		}
		return extensions.OfficialCLI{Kind: kind, Executable: "/managed/" + string(kind)}, true
	}
	var installedKind extensions.OfficialCLIKind
	installOfficialCLI = func(_ context.Context, kind extensions.OfficialCLIKind) (extensions.OfficialCLI, error) {
		installedKind = kind
		return extensions.OfficialCLI{Kind: kind, Version: "1.2.3", Executable: "/installed/" + string(kind)}, nil
	}
	c := &Controller{}
	larkCLI, projectCLI := c.DiscoverLarkCLIs("/existing/lark", "")
	if larkCLI != "/existing/lark-resolved" || projectCLI != "/managed/meegle" {
		t.Fatalf("discovery=%q %q", larkCLI, projectCLI)
	}
	_, _, larkFound, projectFound := c.DetectLarkCLIs("/existing/lark", "")
	if !larkFound || !projectFound {
		t.Fatalf("match flags lark=%t project=%t", larkFound, projectFound)
	}
	path, err := c.InstallOfficialLarkCLI(context.Background(), true)
	if err != nil || installedKind != extensions.OfficialProjectCLI || path != "/installed/meegle" {
		t.Fatalf("install kind=%q path=%q err=%v", installedKind, path, err)
	}
}
