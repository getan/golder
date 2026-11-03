package extensions

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func executableFixture(t *testing.T, dir, name, content string) string {
	t.Helper()
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

func checksumOf(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func TestManagedPackageInstallIsExplicitPrivateAndVerified(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GOLDER_HOME", home)
	if _, ok := InstalledManagedPackage("lark"); ok {
		t.Fatal("package appeared before install")
	}
	if _, err := os.Stat(filepath.Join(home, "extensions")); !os.IsNotExist(err) {
		t.Fatalf("data-only package lookup created state: %v", err)
	}

	const content = "first-party-helper"
	source := executableFixture(t, t.TempDir(), executableName("golder-lark"), content)
	pkg, err := InstallManagedPackage(PackageInstallOptions{
		ID: "lark", Version: "1.2.3", Source: source, SHA256: checksumOf(content) + "  golder-lark",
	})
	if err != nil {
		t.Fatal(err)
	}
	if pkg.ID != "lark" || pkg.Version != "1.2.3" || !strings.HasPrefix(pkg.Root, home+string(filepath.Separator)) {
		t.Fatalf("package = %+v", pkg)
	}
	if info, err := os.Stat(pkg.Executable); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("installed executable: info=%v err=%v", info, err)
	}
	loaded, ok := InstalledManagedPackage("lark")
	if !ok || loaded.SHA256 != checksumOf(content) || loaded.Executable != pkg.Executable {
		t.Fatalf("loaded package = %+v ok=%v", loaded, ok)
	}
}

func TestManagedPackageRejectsWrongChecksumAndBundleSymlink(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	source := executableFixture(t, t.TempDir(), executableName("golder-lark"), "content")
	if _, err := InstallManagedPackage(PackageInstallOptions{ID: "lark", Version: "1", Source: source, SHA256: strings.Repeat("0", 64)}); err == nil {
		t.Fatal("wrong checksum installed")
	}
	link := filepath.Join(t.TempDir(), executableName("golder-lark"))
	if err := os.Symlink(source, link); err == nil {
		if _, err := InstallManagedPackage(PackageInstallOptions{ID: "lark", Version: "2", Source: link, SHA256: checksumOf("content")}); err == nil {
			t.Fatal("symlinked bundle helper installed")
		}
	}
}

func TestManagedPackageRejectsSymlinkedStateComponent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GOLDER_HOME", home)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(home, "extensions")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	const content = "helper"
	source := executableFixture(t, t.TempDir(), executableName("golder-lark"), content)
	if _, err := InstallManagedPackage(PackageInstallOptions{ID: "lark", Version: "1", Source: source, SHA256: checksumOf(content)}); err == nil {
		t.Fatal("installed through a symlinked state component")
	}
	if _, err := os.Stat(filepath.Join(outside, "packages")); !os.IsNotExist(err) {
		t.Fatalf("installer wrote through symlink: %v", err)
	}
}

func TestManagedPackageVersionOrderIsNumeric(t *testing.T) {
	versions := []string{"1.9.0", "1.10.0", "1.10.0-beta", "dev"}
	slices.SortFunc(versions, comparePackageVersions)
	if strings.Join(versions[:3], ",") != "1.9.0,1.10.0-beta,1.10.0" {
		t.Fatalf("version order = %v", versions)
	}
}

func TestRemoveAllManagedPackageVersions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GOLDER_HOME", home)
	for _, version := range []string{"1.9.0", "1.10.0"} {
		content := "helper-" + version
		source := executableFixture(t, t.TempDir(), executableName("golder-lark"), content)
		if _, err := InstallManagedPackage(PackageInstallOptions{ID: "lark", Version: version, Source: source, SHA256: checksumOf(content)}); err != nil {
			t.Fatal(err)
		}
	}
	if installed, ok := InstalledManagedPackage("lark"); !ok || installed.Version != "1.10.0" {
		t.Fatalf("newest package = %+v ok=%v", installed, ok)
	}
	if err := RemoveAllManagedPackages("lark"); err != nil {
		t.Fatal(err)
	}
	if _, ok := InstalledManagedPackage("lark"); ok {
		t.Fatal("package remained after uninstall")
	}
	if _, err := os.Stat(filepath.Join(home, "extensions", "packages", "lark")); !os.IsNotExist(err) {
		t.Fatalf("package base remained: %v", err)
	}
}

func TestLarkSourceConfigContainsReferenceNotSecret(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	const helper = "helper"
	source := executableFixture(t, t.TempDir(), executableName("golder-lark"), helper)
	pkg, err := InstallManagedPackage(PackageInstallOptions{ID: "lark", Version: "dev", Source: source, SHA256: checksumOf(helper)})
	if err != nil {
		t.Fatal(err)
	}
	larkCLI := executableFixture(t, t.TempDir(), "lark-cli", "native-lark")
	projectCLI := executableFixture(t, t.TempDir(), "meegle", "native-project")
	cfg, err := LarkSourceConfig(LarkSourceOptions{
		Package: pkg, LarkCLI: larkCLI, ProjectCLI: projectCLI,
		AppID: "cli_test", AppSecretCredential: "lark-app-secret",
		Domains: []string{"docs", "im"}, ProjectHost: "project.feishu.cn",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Env["LARK_MCP_APP_SECRET"]; got != "${credential:lark-app-secret}" {
		t.Fatalf("secret env = %q", got)
	}
	if cfg.Command != pkg.Executable || len(cfg.Executables) != 2 || len(cfg.SetupVariants["project"]) == 0 || cfg.Enabled {
		t.Fatalf("source config = %+v", cfg)
	}
	if strings.Contains(strings.Join(cfg.Setup, " "), "native-lark") {
		t.Fatal("credential-like fixture content entered argv")
	}
}

func TestLarkSourceConfigRejectsInvalidIdentityAndHost(t *testing.T) {
	t.Setenv("GOLDER_HOME", t.TempDir())
	const helper = "helper"
	source := executableFixture(t, t.TempDir(), executableName("golder-lark"), helper)
	pkg, err := InstallManagedPackage(PackageInstallOptions{ID: "lark", Version: "dev", Source: source, SHA256: checksumOf(helper)})
	if err != nil {
		t.Fatal(err)
	}
	larkCLI := executableFixture(t, t.TempDir(), "lark-cli", "native-lark")
	base := LarkSourceOptions{Package: pkg, LarkCLI: larkCLI, AppID: "cli_test", AppSecretCredential: "lark-app-secret"}
	for name, mutate := range map[string]func(*LarkSourceOptions){
		"credential syntax": func(o *LarkSourceOptions) { o.AppSecretCredential = "bad}name" },
		"app id":            func(o *LarkSourceOptions) { o.AppID = "bad id" },
		"host labels":       func(o *LarkSourceOptions) { o.ProjectHost = "bad..example.com" },
		"ip host":           func(o *LarkSourceOptions) { o.ProjectHost = "127.0.0.1" },
	} {
		t.Run(name, func(t *testing.T) {
			copy := base
			mutate(&copy)
			if _, err := LarkSourceConfig(copy); err == nil {
				t.Fatal("invalid Lark source was accepted")
			}
		})
	}
}

func TestConfigureSourceAddsDisabledUnapprovedRuntimeEntry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GOLDER_HOME", home)
	configPath := filepath.Join(home, "config.toml")
	manager, err := New(Options{ConfigPath: configPath, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	exe, _ := os.Executable()
	ref := SourceRef{Kind: "mcp", ID: "dynamic"}
	cfg := SourceConfig{Transport: "stdio", Command: exe}
	if err := WriteSource(configPath, ref, &cfg); err != nil {
		t.Fatal(err)
	}
	if err := manager.ConfigureSource(ref, cfg); err != nil {
		t.Fatal(err)
	}
	snapshot, ok := manager.Source(ref)
	if !ok || snapshot.Enabled || snapshot.Trust == "approved" {
		t.Fatalf("dynamic snapshot = %+v ok=%v", snapshot, ok)
	}
	if err := manager.Approve(ref); err != nil {
		t.Fatal(err)
	}
	if err := manager.EnabledSourceConfig(ref, true); err != nil {
		t.Fatal(err)
	}
	if err := manager.Enable(ref); err != nil {
		t.Fatal(err)
	}
	if snapshot, _ = manager.Source(ref); !snapshot.Enabled || snapshot.Trust != "approved" {
		t.Fatalf("enabled snapshot = %+v", snapshot)
	}
}
