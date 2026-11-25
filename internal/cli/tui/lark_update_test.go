package tui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getan/golder/internal/extensions"
)

func larkUpdateFixturePackage(t *testing.T, version, content string) extensions.ManagedPackage {
	t.Helper()
	source := filepath.Join(t.TempDir(), "golder-lark")
	if err := os.WriteFile(source, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	pkg, err := extensions.InstallManagedPackage(extensions.PackageInstallOptions{
		ID: "lark", Version: version, Source: source, SHA256: hex.EncodeToString(sum[:]),
	})
	if err != nil {
		t.Fatal(err)
	}
	return pkg
}

func larkUpdateModel(t *testing.T) Model {
	t.Helper()
	m := extensionModel(t)
	oldPkg := larkUpdateFixturePackage(t, "1.0.0", "helper-v1")
	cli := filepath.Join(t.TempDir(), "lark-cli")
	if err := os.WriteFile(cli, []byte("official-cli"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg, err := extensions.LarkSourceConfig(extensions.LarkSourceOptions{
		Package: oldPkg, LarkCLI: cli, AppID: "cli_test", AppSecretCredential: "lark-app-secret",
		Domains: []string{"docs"}, ProjectHost: "project.feishu.cn",
	})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(os.Getenv("GOLDER_HOME"), "config.toml")
	if err := extensions.WriteSource(configPath, extensions.LarkSourceRef, &cfg); err != nil {
		t.Fatal(err)
	}
	if err := m.opts.Extensions.ConfigureSource(extensions.LarkSourceRef, cfg); err != nil {
		t.Fatal(err)
	}
	if err := m.opts.Extensions.Approve(extensions.LarkSourceRef); err != nil {
		t.Fatal(err)
	}
	if err := m.opts.Extensions.Enable(extensions.LarkSourceRef); err != nil {
		t.Fatal(err)
	}
	return m
}

func stubLarkRelease(t *testing.T, version string) {
	t.Helper()
	old := larkReleaseFor
	larkReleaseFor = func(string) (extensions.LarkRelease, error) {
		return extensions.LarkRelease{
			Version: version, Archive: "golder-lark.tar.gz",
			ArchiveURL:  "https://example.invalid/golder-lark.tar.gz",
			ChecksumURL: "https://example.invalid/checksums.txt",
		}, nil
	}
	t.Cleanup(func() { larkReleaseFor = old })
}

func stubLarkInstall(t *testing.T, fn func(context.Context, string) (extensions.ManagedPackage, error)) {
	t.Helper()
	old := installLarkRelease
	installLarkRelease = fn
	t.Cleanup(func() { installLarkRelease = old })
}

func TestLarkHelperUpdateAppearsOnlyOnVersionMismatch(t *testing.T) {
	m := larkUpdateModel(t)
	stubLarkRelease(t, "1.1.0")
	next, _ := m.extensionDetail(extensions.LarkSourceRef)
	m = next.(Model)
	view := m.View().Content
	if !strings.Contains(view, "Update helper 1.0.0 → 1.1.0") {
		t.Fatalf("update entry missing: %s", view)
	}
	if !strings.Contains(view, "Helper") || !strings.Contains(view, "1.0.0 · 1.1.0 available") {
		t.Fatalf("helper version line missing: %s", view)
	}

	old := larkReleaseFor
	larkReleaseFor = func(string) (extensions.LarkRelease, error) {
		return extensions.LarkRelease{}, errors.New("dev build")
	}
	t.Cleanup(func() { larkReleaseFor = old })
	next, _ = m.extensionDetail(extensions.LarkSourceRef)
	if view := next.(Model).View().Content; strings.Contains(view, "Update helper") {
		t.Fatalf("dev build offered an update: %s", view)
	}
}

func TestLarkHelperUpdateKeepsOldHelperOnDownloadFailure(t *testing.T) {
	m := larkUpdateModel(t)
	stubLarkRelease(t, "1.1.0")
	downloads := 0
	stubLarkInstall(t, func(context.Context, string) (extensions.ManagedPackage, error) {
		downloads++
		return extensions.ManagedPackage{}, errors.New("network unreachable")
	})

	next, _ := m.extensionDetail(extensions.LarkSourceRef)
	m = next.(Model)
	next, cmd := m.chooseExtension(extensionValue("lark-helper-update"))
	m = next.(Model)
	if cmd != nil || m.larkUpdate == nil || m.larkUpdate.phase != "confirm" || downloads != 0 {
		t.Fatal("opening the confirmation started a download")
	}
	if view := m.View().Content; !strings.Contains(view, "https://example.invalid/golder-lark.tar.gz") ||
		!strings.Contains(view, "SHA-256") ||
		!strings.Contains(view, "reviewed again") {
		t.Fatalf("confirmation page is incomplete: %s", view)
	}

	next, cmd = m.chooseExtension(extensionValue("lark-helper-confirm"))
	m = next.(Model)
	if cmd == nil || m.larkUpdate.phase != "installing" {
		t.Fatal("confirmation did not schedule the download")
	}
	m = apply(t, m, cmd())
	if m.larkUpdate.phase != "failed" || !strings.Contains(m.View().Content, "network unreachable") {
		t.Fatalf("failure was not reported: %+v %s", m.larkUpdate, m.View().Content)
	}
	if !strings.Contains(m.View().Content, "Retry") || !strings.Contains(m.View().Content, "Existing helper unchanged") {
		t.Fatalf("failure page lost retry guidance: %s", m.View().Content)
	}
	cfg, err := m.opts.Extensions.Config(extensions.LarkSourceRef)
	if err != nil || !strings.Contains(cfg.Command, filepath.Join("lark", "1.0.0")) {
		t.Fatalf("failed update changed the command: %q %v", cfg.Command, err)
	}
	snapshot, _ := m.opts.Extensions.Source(extensions.LarkSourceRef)
	if !snapshot.Enabled || snapshot.Trust != "approved" {
		t.Fatalf("failed update revoked the working source: %+v", snapshot)
	}
}

func TestLarkHelperUpdateRebindsAndDisablesSource(t *testing.T) {
	m := larkUpdateModel(t)
	m.opts.Version = "1.5.138"
	stubLarkRelease(t, "1.1.0")
	newPkg := larkUpdateFixturePackage(t, "1.1.0", "helper-v2")
	var installedVersion string
	stubLarkInstall(t, func(_ context.Context, version string) (extensions.ManagedPackage, error) {
		installedVersion = version
		return newPkg, nil
	})

	next, _ := m.extensionDetail(extensions.LarkSourceRef)
	m = next.(Model)
	next, _ = m.chooseExtension(extensionValue("lark-helper-update"))
	m = next.(Model)
	next, cmd := m.chooseExtension(extensionValue("lark-helper-confirm"))
	m = next.(Model)
	m = apply(t, m, cmd())

	if m.larkUpdate.phase != "done" {
		t.Fatalf("update did not finish: %+v", m.larkUpdate)
	}
	if installedVersion != "1.5.138" {
		t.Fatalf("installer received %q, want the host version 1.5.138", installedVersion)
	}
	cfg, err := m.opts.Extensions.Config(extensions.LarkSourceRef)
	if err != nil || cfg.Command != newPkg.Executable {
		t.Fatalf("command = %q, want %q", cfg.Command, newPkg.Executable)
	}
	if cfg.Env["LARK_MCP_APP_SECRET"] != "${credential:lark-app-secret}" || cfg.Env["LARK_MCP_APP_ID"] != "cli_test" {
		t.Fatalf("settings were not preserved: %+v", cfg.Env)
	}
	snapshot, _ := m.opts.Extensions.Source(extensions.LarkSourceRef)
	if snapshot.Enabled || snapshot.Trust == "approved" {
		t.Fatalf("new helper bytes were approved automatically: %+v", snapshot)
	}
	if view := m.View().Content; !strings.Contains(view, "Source disabled; review to enable") {
		t.Fatalf("completion did not ask for review: %s", view)
	}
}

func TestLarkHelperGateBlocksStableBuildsAndWarnsOnDev(t *testing.T) {
	m := larkUpdateModel(t)
	stubLarkRelease(t, "1.1.0")
	next, _ := m.chooseExtension(extensionValue("review", "mcp", "lark"))
	m = next.(Model)
	if m.mcpWizard != nil {
		t.Fatal("a stale helper reached the approval dialog")
	}
	if !strings.Contains(m.View().Content, "differs from "+extensions.LarkHelperVersion()) || !strings.Contains(m.View().Content, "update it first") {
		t.Fatalf("block message missing: %s", m.View().Content)
	}

	old := larkReleaseFor
	larkReleaseFor = func(string) (extensions.LarkRelease, error) {
		return extensions.LarkRelease{}, errors.New("dev build")
	}
	t.Cleanup(func() { larkReleaseFor = old })
	next, _ = m.chooseExtension(extensionValue("review", "mcp", "lark"))
	m = next.(Model)
	if m.mcpWizard == nil {
		t.Fatalf("a dev build was hard-blocked: %s", m.View().Content)
	}
	if !strings.Contains(strings.Join(m.transcript.contentLines(), "\n"), "no installable release") {
		t.Fatalf("dev warning missing: %s", strings.Join(m.transcript.contentLines(), "\n"))
	}
}
