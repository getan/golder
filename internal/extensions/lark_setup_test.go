package extensions

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/provider"
)

func TestAddLarkIsDisabledAndProtectsSecret(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GOLDER_HOME", home)
	const helper = "fixture-lark-helper"
	source := executableFixture(t, t.TempDir(), executableName("golder-lark"), helper)
	pkg, err := InstallManagedPackage(PackageInstallOptions{ID: "lark", Version: "1.2.3", Source: source, SHA256: checksumOf(helper)})
	if err != nil {
		t.Fatal(err)
	}
	cli := executableFixture(t, t.TempDir(), "lark-cli", "fixture-cli")
	manager, err := New(Options{ConfigPath: filepath.Join(home, "config.toml"), Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	secret := "fixture-" + "lark-private-value"
	setup := LarkSetup{Package: pkg, LarkCLI: cli, AppID: "cli_test", AppSecret: secret, Domains: []string{"docs", "drive", "im", "sheets", "base"}}
	if err := manager.AddLark(context.Background(), setup); err != nil {
		t.Fatal(err)
	}
	ref := SourceRef{Kind: "mcp", ID: "lark"}
	view, ok := manager.Source(ref)
	if !ok || view.Enabled || view.Trust == "approved" {
		t.Fatalf("Lark was not saved disabled: %+v", view)
	}
	cfg, err := manager.Config(ref)
	if err != nil || cfg.Env["LARK_MCP_DOMAINS"] != "docs,drive,im,sheets,base" {
		t.Fatalf("incorrect saved Lark configuration: %v", err)
	}
	resolved, err := resolveValue(cfg.Env["LARK_MCP_APP_SECRET"])
	if err != nil || resolved != secret {
		t.Fatal("named Lark credential did not resolve")
	}
	raw, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if err != nil || strings.Contains(string(raw), secret) {
		t.Fatal("Lark secret was written into configuration")
	}
	before, err := provider.CredentialNames(provider.CredentialFilePath())
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.AddLark(context.Background(), setup); err == nil {
		t.Fatal("existing Lark source was overwritten")
	}
	after, err := provider.CredentialNames(provider.CredentialFilePath())
	if err != nil || len(after) != len(before) {
		t.Fatal("failed duplicate setup left an unused credential")
	}
}
