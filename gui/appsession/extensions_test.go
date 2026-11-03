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
		Model: "test-model", ProviderName: "test-provider", Cwd: t.TempDir(), Store: store,
		Extensions: manager,
	})
	if err := controller.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer controller.Close()

	const helperContent = "managed-lark-helper"
	helper := guiExtensionExecutable(t, t.TempDir(), "golder-lark", helperContent)
	sum := sha256.Sum256([]byte(helperContent))
	if err := controller.InstallLarkPackage(context.Background(), helper, hex.EncodeToString(sum[:]), "test"); err != nil {
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
