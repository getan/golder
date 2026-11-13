package toolchain

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoManifestSeparatesBuildAndDownloadCapabilities(t *testing.T) {
	root := t.TempDir()
	env := GoPaths{GOROOT: filepath.Join(root, "sdk"), GOMODCACHE: filepath.Join(root, "modules"), GOCACHE: filepath.Join(root, "build")}
	build, err := BuildGo("go", "/usr/local/go/bin/go", "binary", env)
	if err != nil {
		t.Fatal(err)
	}
	download, err := BuildGo("go-download", "/usr/local/go/bin/go", "binary", env)
	if err != nil {
		t.Fatal(err)
	}
	if len(build.WriteDirs) != 1 || len(download.WriteDirs) != 2 || build.Binding == download.Binding {
		t.Fatalf("incorrect profiles: %+v %+v", build, download)
	}
	env.GOCACHE = filepath.Join(root, "another-cache")
	changed, err := BuildGo("go", "/usr/local/go/bin/go", "binary", env)
	if err != nil || changed.Binding == build.Binding {
		t.Fatal("cache change did not invalidate binding")
	}
}

func TestGoProbeDoesNotExecuteWorkspaceShims(t *testing.T) {
	workspace := t.TempDir()
	marker := filepath.Join(workspace, "executed")
	shim := "#!/bin/sh\ntouch " + marker + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(workspace, "go"), []byte(shim), 0700); err != nil {
		t.Fatal(err)
	}
	_, _ = findInstalledGo(workspace, workspace)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("workspace Go shim was executed")
	}
	if installedGoPath(filepath.Join(workspace, "go")) {
		t.Fatal("temporary executable was classified as an installed SDK")
	}
}

func TestGoProbeEnvironmentCannotDownloadOrInjectCode(t *testing.T) {
	env := probeEnvironment([]string{"HOME=/home/operator", "GOTOOLCHAIN=auto", "GOPROXY=https://example.invalid", "GOFLAGS=-toolexec=evil", "DYLD_INSERT_LIBRARIES=evil", "GOCACHEPROG=evil"})
	joined := strings.Join(env, "\n")
	for _, value := range []string{"evil", "GOTOOLCHAIN=auto", "https://example.invalid"} {
		if strings.Contains(joined, value) {
			t.Fatalf("probe inherited unsafe configuration %q", value)
		}
	}
}

func TestGoInstalledProbeLive(t *testing.T) {
	if os.Getenv("GOLDER_GO_PROFILE_TEST") != "1" {
		t.Skip("opt-in installed Go path probe")
	}
	p, err := Resolve(context.Background(), t.TempDir(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.ReadDirs) != 2 || len(p.WriteDirs) != 1 || p.Binding == "" {
		t.Fatalf("invalid installed profile: %+v", p)
	}
	t.Logf("resolved installed Go %s; read directories=%d write directories=%d", p.Executable, len(p.ReadDirs), len(p.WriteDirs))
}

func TestGoManifestRejectsWideOrCredentialPaths(t *testing.T) {
	home, _ := os.UserHomeDir()
	for _, bad := range []string{"/", home, filepath.Join(home, ".ssh"), filepath.Join(home, ".gnupg"), "relative", "off"} {
		_, err := BuildGo("go", "/usr/local/go/bin/go", "binary", GoPaths{GOROOT: "/usr/local/go", GOMODCACHE: filepath.Join(t.TempDir(), "modules"), GOCACHE: bad})
		if err == nil {
			t.Fatalf("accepted unsafe cache %q", bad)
		}
	}
}

func TestGoManifestCannotTurnReadsIntoSDKOrModuleWrites(t *testing.T) {
	root := t.TempDir()
	for _, cache := range []string{"/usr", filepath.Join(root, "sdk"), filepath.Join(root, "modules"), root} {
		_, err := BuildGo("go", "/usr/local/go/bin/go", "identity", GoPaths{GOROOT: filepath.Join(root, "sdk"), GOMODCACHE: filepath.Join(root, "modules"), GOCACHE: cache})
		if err == nil {
			t.Fatalf("unsafe cache admitted: %q", cache)
		}
	}
}
