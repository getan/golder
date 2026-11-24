package extensioncmd

import (
	"bytes"
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

func larkPackageFixture(t *testing.T, version, content string) extensions.ManagedPackage {
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

func configureLarkFixture(t *testing.T, version string) {
	t.Helper()
	pkg := larkPackageFixture(t, version, "helper-"+version)
	cli := filepath.Join(t.TempDir(), "lark-cli")
	if err := os.WriteFile(cli, []byte("official-cli"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg, err := extensions.LarkSourceConfig(extensions.LarkSourceOptions{
		Package: pkg, LarkCLI: cli, AppID: "cli_test", AppSecretCredential: "lark-app-secret",
		Domains: []string{"docs"}, ProjectHost: "project.feishu.cn",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := extensions.WriteSource(extensions.ConfigPath(), extensions.LarkSourceRef, &cfg); err != nil {
		t.Fatal(err)
	}
}

func stubLarkAdminSeams(t *testing.T, version string, install func(context.Context, string) (extensions.ManagedPackage, error)) *extensions.ManagedPackage {
	t.Helper()
	oldInstall, oldRebind, oldRelease := installLarkRelease, rebindLarkSource, larkReleaseFor
	t.Cleanup(func() { installLarkRelease, rebindLarkSource, larkReleaseFor = oldInstall, oldRebind, oldRelease })
	rebound := new(extensions.ManagedPackage)
	larkReleaseFor = func(string) (extensions.LarkRelease, error) {
		return extensions.LarkRelease{Version: version, ArchiveURL: "https://example.invalid/golder-lark.tar.gz", ChecksumURL: "https://example.invalid/checksums.txt"}, nil
	}
	installLarkRelease = install
	rebindLarkSource = func(_ *extensions.Manager, pkg extensions.ManagedPackage) error {
		*rebound = pkg
		return nil
	}
	return rebound
}

func TestUpdateHelperNeedsConfirmation(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	configureLarkFixture(t, "1.0.0")
	installs := 0
	stubLarkAdminSeams(t, "1.1.0", func(context.Context, string) (extensions.ManagedPackage, error) {
		installs++
		return extensions.ManagedPackage{}, errors.New("must not install")
	})
	var out, errs bytes.Buffer
	if code := RunVersion("mcp", "1.5.135", []string{"update-helper", "lark"}, strings.NewReader("no\n"), &out, &errs); code == 0 {
		t.Fatal("an unconfirmed update succeeded")
	}
	if installs != 0 {
		t.Fatal("downloaded before confirmation")
	}
	if !strings.Contains(errs.String(), "update not confirmed") {
		t.Fatalf("confirmation error missing: %s", errs.String())
	}
}

func TestUpdateHelperInstallsAndRebinds(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	configureLarkFixture(t, "1.0.0")
	newPkg := larkPackageFixture(t, "1.1.0", "helper-v2")
	rebound := stubLarkAdminSeams(t, "1.1.0", func(context.Context, string) (extensions.ManagedPackage, error) {
		return newPkg, nil
	})
	var out, errs bytes.Buffer
	if code := RunVersion("mcp", "1.5.135", []string{"update-helper", "lark", "--yes"}, strings.NewReader(""), &out, &errs); code != 0 {
		t.Fatalf("update failed: %s", errs.String())
	}
	if rebound.Executable != newPkg.Executable {
		t.Fatalf("rebound package = %+v", *rebound)
	}
	if !strings.Contains(out.String(), "source disabled") {
		t.Fatalf("completion did not require review: %s", out.String())
	}
}

func TestStaleLarkHelperBlocksApproval(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	configureLarkFixture(t, "1.0.0")
	stubLarkAdminSeams(t, "1.1.0", func(context.Context, string) (extensions.ManagedPackage, error) {
		return extensions.ManagedPackage{}, errors.New("unused")
	})
	var out, errs bytes.Buffer
	if code := RunVersion("mcp", "1.5.135", []string{"enable", "lark", "--yes"}, strings.NewReader(""), &out, &errs); code == 0 {
		t.Fatal("a stale helper was enabled")
	}
	if !strings.Contains(errs.String(), "update-helper") {
		t.Fatalf("block did not point at the update action: %s", errs.String())
	}

	larkReleaseFor = func(string) (extensions.LarkRelease, error) {
		return extensions.LarkRelease{}, errors.New("dev build")
	}
	out.Reset()
	errs.Reset()
	_ = RunVersion("mcp", "dev", []string{"enable", "lark", "--yes"}, strings.NewReader(""), &out, &errs)
	if !strings.Contains(errs.String(), "warning: Lark helper") {
		t.Fatalf("a dev build was hard-blocked: %s", errs.String())
	}
}
