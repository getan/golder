package extensions

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func officialCLITestArchive(t *testing.T, path string, binary []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: path, Mode: 0o755, Size: int64(len(binary)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(binary); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestOfficialCLIInstallerVerifiesExtractsAndDiscovers(t *testing.T) {
	binary := append([]byte{0xfe, 0xed, 0xfa, 0xcf}, bytes.Repeat([]byte{0x42}, 128)...)
	for _, tc := range []struct {
		name        string
		kind        OfficialCLIKind
		archivePath string
		binaryName  string
		sha512      bool
	}{
		{"lark sha256", OfficialLarkCLI, "lark-cli", "lark-cli", false},
		{"meegle npm sri", OfficialProjectCLI, "package/bin/meegle-darwin-arm64", "meegle", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := officialCLITestArchive(t, tc.archivePath, binary)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archive) }))
			defer srv.Close()
			spec := OfficialCLIInstallSpec{Version: "1.2.3", ArchiveURL: srv.URL + "/cli.tgz", ArchivePath: tc.archivePath, BinaryName: tc.binaryName}
			if tc.sha512 {
				sum := sha512.Sum512(archive)
				spec.SHA512 = base64.StdEncoding.EncodeToString(sum[:])
			} else {
				sum := sha256.Sum256(archive)
				spec.SHA256 = hex.EncodeToString(sum[:])
			}
			installer := &OfficialCLIInstaller{
				Client: srv.Client(), Root: t.TempDir(), GOOS: "darwin", GOARCH: "arm64", AllowHTTP: true,
				Specs: map[OfficialCLIKind]OfficialCLIInstallSpec{tc.kind: spec},
			}
			installed, err := installer.Install(context.Background(), tc.kind)
			if err != nil {
				t.Fatal(err)
			}
			if installed.Version != "1.2.3" || installed.Executable == "" {
				t.Fatalf("installed=%+v", installed)
			}
			if data, err := os.ReadFile(installed.Executable); err != nil || !bytes.Equal(data, binary) {
				t.Fatalf("binary=%x err=%v", data, err)
			}
			found, ok := installer.Discover(tc.kind, "")
			resolved, _ := filepath.EvalSymlinks(installed.Executable)
			if !ok || found.Executable != resolved {
				t.Fatalf("discovered=%+v ok=%v", found, ok)
			}
		})
	}
}

func TestOfficialCLIInstallerRejectsChecksumMismatchAndWrappers(t *testing.T) {
	binary := append([]byte{0x7f, 'E', 'L', 'F'}, bytes.Repeat([]byte{1}, 32)...)
	archive := officialCLITestArchive(t, "lark-cli", binary)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archive) }))
	defer srv.Close()
	root := t.TempDir()
	installer := &OfficialCLIInstaller{
		Client: srv.Client(), Root: root, GOOS: "linux", GOARCH: "amd64", AllowHTTP: true,
		SkipCommonDiscovery: true,
		Specs: map[OfficialCLIKind]OfficialCLIInstallSpec{OfficialLarkCLI: {
			Version: "1.0.0", ArchiveURL: srv.URL + "/cli.tar.gz", ArchivePath: "lark-cli", BinaryName: "lark-cli", SHA256: "00",
		}},
	}
	if _, err := installer.Install(context.Background(), OfficialLarkCLI); err == nil {
		t.Fatal("checksum mismatch was accepted")
	}
	if _, err := os.Stat(filepath.Join(root, string(OfficialLarkCLI), "1.0.0")); !os.IsNotExist(err) {
		t.Fatalf("checksum failure created an install: %v", err)
	}
	wrapper := filepath.Join(t.TempDir(), "lark-cli")
	if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	installer.Specs[OfficialLarkCLI] = OfficialCLIInstallSpec{Version: "2.0.0", BinaryName: "lark-cli"}
	if found, ok := installer.Discover(OfficialLarkCLI, wrapper); ok || found.Executable != "" {
		t.Fatalf("script wrapper discovered as native: %+v", found)
	}
}

func TestPinnedOfficialCLIManifestsCoverCurrentPlatform(t *testing.T) {
	specs := officialCLISpecs(runtime.GOOS, runtime.GOARCH)
	for _, kind := range []OfficialCLIKind{OfficialLarkCLI, OfficialProjectCLI} {
		spec := specs[kind]
		if spec.ArchiveURL == "" || spec.ArchivePath == "" || spec.BinaryName == "" || (spec.SHA256 == "" && spec.SHA512 == "") {
			t.Fatalf("%s spec is incomplete: %+v", kind, spec)
		}
	}
}

func TestOfficialCLIDiscoveryFindsLocalNativePackageBinary(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", "")
	spec := officialCLISpecs(runtime.GOOS, runtime.GOARCH)[OfficialProjectCLI]
	path := filepath.Join(root, "node_modules", "@lark-project", "meegle", "bin", filepath.Base(spec.ArchivePath))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	binary := append([]byte{0x7f, 'E', 'L', 'F'}, bytes.Repeat([]byte{1}, 32)...)
	if err := os.WriteFile(path, binary, 0o700); err != nil {
		t.Fatal(err)
	}
	installer := &OfficialCLIInstaller{
		Root: t.TempDir(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		Specs: officialCLISpecs(runtime.GOOS, runtime.GOARCH),
	}
	found, ok := installer.Discover(OfficialProjectCLI, "")
	if !ok {
		t.Fatal("local native Project CLI was not discovered")
	}
	resolved, _ := filepath.EvalSymlinks(path)
	if found.Executable != resolved {
		t.Fatalf("found=%q want=%q", found.Executable, resolved)
	}
}
